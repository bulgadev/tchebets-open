package store

import (
	"database/sql"
	"errors"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

var ErrAccountNotFound = errors.New("account not found")
var ErrEmailTaken = errors.New("email already registered")

type Account struct {
	ID           string
	Email        string
	PasswordHash string
	IsAdmin      bool
	CreatedAt    time.Time
}

// CreateAccount inserts a new account. Admin bootstrap is fail-closed: the
// only account that becomes admin automatically is the one matching
// INITIAL_ADMIN_EMAIL (unset by default, so nobody does). Every admin after
// that is made via PromoteToAdmin - replaces the old "first account ever
// created becomes admin" prototype shortcut, which had to go before Phase 3
// touched a peer-to-peer money feature (see docs/roadmap.md).
func CreateAccount(id, email, passwordHash string) (*Account, error) {
	email = strings.ToLower(strings.TrimSpace(email))

	initialAdminEmail := strings.ToLower(strings.TrimSpace(os.Getenv("INITIAL_ADMIN_EMAIL")))
	isAdmin := initialAdminEmail != "" && email == initialAdminEmail

	var createdAt time.Time
	err := DB.QueryRow(
		`INSERT INTO accounts (id, email, password_hash, is_admin, created_at)
		 VALUES ($1, $2, $3, $4, NOW())
		 RETURNING created_at`,
		id, email, passwordHash, isAdmin,
	).Scan(&createdAt)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, ErrEmailTaken
		}
		return nil, err
	}

	return &Account{ID: id, Email: email, PasswordHash: passwordHash, IsAdmin: isAdmin, CreatedAt: createdAt}, nil
}

func FindAccountByEmail(email string) (*Account, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	var a Account
	err := DB.QueryRow(
		`SELECT id, email, password_hash, is_admin, created_at FROM accounts WHERE email = $1`,
		email,
	).Scan(&a.ID, &a.Email, &a.PasswordHash, &a.IsAdmin, &a.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrAccountNotFound
	}
	if err != nil {
		return nil, err
	}
	return &a, nil
}

// PromoteToAdmin flips is_admin on for the given email - the only path to
// admin status after the initial bootstrap account (see CreateAccount).
func PromoteToAdmin(email string) error {
	email = strings.ToLower(strings.TrimSpace(email))
	res, err := DB.Exec(`UPDATE accounts SET is_admin = TRUE WHERE email = $1`, email)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrAccountNotFound
	}
	return nil
}

// ListAccounts returns every account, oldest first, for the admin user-management page.
func ListAccounts() ([]*Account, error) {
	rows, err := DB.Query(`SELECT id, email, password_hash, is_admin, created_at FROM accounts ORDER BY created_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	accounts := make([]*Account, 0)
	for rows.Next() {
		var a Account
		if err := rows.Scan(&a.ID, &a.Email, &a.PasswordHash, &a.IsAdmin, &a.CreatedAt); err != nil {
			return nil, err
		}
		accounts = append(accounts, &a)
	}
	return accounts, nil
}

func FindAccountByID(id string) (*Account, error) {
	var a Account
	err := DB.QueryRow(
		`SELECT id, email, password_hash, is_admin, created_at FROM accounts WHERE id = $1`,
		id,
	).Scan(&a.ID, &a.Email, &a.PasswordHash, &a.IsAdmin, &a.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrAccountNotFound
	}
	if err != nil {
		return nil, err
	}
	return &a, nil
}

// isUniqueViolation checks for Postgres unique_violation (SQLSTATE 23505).
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}