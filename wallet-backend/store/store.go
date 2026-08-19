// Package store is wallet-backend's data access: the address<->account mapping
// and the append-only deposit/withdrawal ledgers. It holds no crypto and no
// balances - just custody bookkeeping. The Store is a thin object over *sql.DB
// so the service layer has a testable seam (see the interfaces the service
// declares over these methods).
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"tchebet/wallet-backend/models"

	"github.com/google/uuid"
)

// ErrNotFound is returned when a lookup has no row - lets the service tell
// "this account has no address yet" apart from a real DB error.
var ErrNotFound = errors.New("not found")

type Store struct {
	db *sql.DB
}

func New(db *sql.DB) *Store {
	return &Store{db: db}
}

// NextDerivationIndex draws the next stable index from the sequence. Gaps (from
// a provisioning attempt that later fails) are harmless: indices only need to
// be unique and stable, not contiguous.
func (s *Store) NextDerivationIndex(ctx context.Context) (uint32, error) {
	var idx int64
	if err := s.db.QueryRowContext(ctx, `SELECT nextval('wallet_derivation_index_seq')`).Scan(&idx); err != nil {
		return 0, fmt.Errorf("allocating derivation index: %w", err)
	}
	return uint32(idx), nil
}

// GetAddressByAccount returns the account's provisioned address, or ErrNotFound.
func (s *Store) GetAddressByAccount(ctx context.Context, accountID string) (*models.WalletAddress, error) {
	var w models.WalletAddress
	err := s.db.QueryRowContext(ctx,
		`SELECT account_id, derivation_index, address, usdc_ata, created_at
		 FROM wallet_addresses WHERE account_id = $1`, accountID).
		Scan(&w.AccountID, &w.DerivationIndex, &w.Address, &w.USDCATA, &w.CreatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &w, nil
}

// GetAddressByATA reverse-looks-up which account a USDC ATA belongs to - the
// deposit path uses it to map a watched token account back to an account.
func (s *Store) GetAddressByATA(ctx context.Context, ata string) (*models.WalletAddress, error) {
	var w models.WalletAddress
	err := s.db.QueryRowContext(ctx,
		`SELECT account_id, derivation_index, address, usdc_ata, created_at
		 FROM wallet_addresses WHERE usdc_ata = $1`, ata).
		Scan(&w.AccountID, &w.DerivationIndex, &w.Address, &w.USDCATA, &w.CreatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &w, nil
}

// GetAddressBySOL reverse-looks-up which account a native SOL address belongs
// to (SOL deposits land on the base address, not an ATA).
func (s *Store) GetAddressBySOL(ctx context.Context, address string) (*models.WalletAddress, error) {
	var w models.WalletAddress
	err := s.db.QueryRowContext(ctx,
		`SELECT account_id, derivation_index, address, usdc_ata, created_at
		 FROM wallet_addresses WHERE address = $1`, address).
		Scan(&w.AccountID, &w.DerivationIndex, &w.Address, &w.USDCATA, &w.CreatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &w, nil
}

// ListAddresses returns every provisioned address - the deposit poller walks
// these each pass.
func (s *Store) ListAddresses(ctx context.Context) ([]models.WalletAddress, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT account_id, derivation_index, address, usdc_ata, created_at
		 FROM wallet_addresses ORDER BY derivation_index`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []models.WalletAddress
	for rows.Next() {
		var w models.WalletAddress
		if err := rows.Scan(&w.AccountID, &w.DerivationIndex, &w.Address, &w.USDCATA, &w.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// GetScanCursor returns the last scanned signature for an address, or "" if the
// poller has never scanned it (scan from the beginning / recent window).
func (s *Store) GetScanCursor(ctx context.Context, address string) (string, error) {
	var sig string
	err := s.db.QueryRowContext(ctx,
		`SELECT last_sig FROM deposit_cursors WHERE address = $1`, address).Scan(&sig)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		return "", err
	}
	return sig, nil
}

// SetScanCursor advances an address's poll cursor to the newest scanned sig.
func (s *Store) SetScanCursor(ctx context.Context, address, lastSig string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO deposit_cursors (address, last_sig, updated_at)
		 VALUES ($1, $2, NOW())
		 ON CONFLICT (address) DO UPDATE SET last_sig = EXCLUDED.last_sig, updated_at = NOW()`,
		address, lastSig)
	return err
}

// InsertAddress records a newly provisioned address. The account_id PRIMARY KEY
// makes a concurrent double-provision fail on the second insert.
func (s *Store) InsertAddress(ctx context.Context, w models.WalletAddress) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO wallet_addresses (account_id, derivation_index, address, usdc_ata)
		 VALUES ($1, $2, $3, $4)`,
		w.AccountID, w.DerivationIndex, w.Address, w.USDCATA)
	return err
}

// ClaimFaucet atomically records a one-time demo faucet claim for an account,
// returning false if the account has already claimed. The account_id PRIMARY KEY
// ON CONFLICT DO NOTHING is the guard: concurrent double-clicks can only ever
// insert one row, so treasury can't be drained by repeat requests. A claim that
// later fails on-chain is released with ReleaseFaucet so the user can retry.
func (s *Store) ClaimFaucet(ctx context.Context, accountID string) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO faucet_claims (account_id) VALUES ($1) ON CONFLICT (account_id) DO NOTHING`,
		accountID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n == 1, nil
}

// ReleaseFaucet removes a faucet claim so the account can retry - used only when
// the on-chain grant transfer failed after the claim row was inserted.
func (s *Store) ReleaseFaucet(ctx context.Context, accountID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM faucet_claims WHERE account_id = $1`, accountID)
	return err
}

// InsertDepositIfNew records a detected deposit, returning false if a row for
// this signature already exists. This UNIQUE(signature) ON CONFLICT DO NOTHING
// is the idempotency guard: a replayed webhook can never create a second
// crediting task for the same on-chain transaction.
func (s *Store) InsertDepositIfNew(ctx context.Context, d models.Deposit) (bool, error) {
	if d.ID == "" {
		d.ID = uuid.New().String()
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO deposits (id, signature, account_id, asset, raw_amount, credited_usdc, status)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)
		 ON CONFLICT (signature) DO NOTHING`,
		d.ID, d.Signature, d.AccountID, string(d.Asset), d.RawAmount, d.CreditedUSDC, string(d.Status))
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n == 1, nil
}

// SetDepositStatus moves a deposit to an intermediate state (e.g. SWAPPING).
func (s *Store) SetDepositStatus(ctx context.Context, signature string, status models.DepositStatus) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE deposits SET status = $1 WHERE signature = $2`, string(status), signature)
	return err
}

// MarkDepositCredited records the on-chain USDC value (micro-USDC) and the
// ledger amount actually credited (whole BRL) plus the terminal CREDITED status.
func (s *Store) MarkDepositCredited(ctx context.Context, signature string, creditedUSDC, creditedReais int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE deposits SET status = $1, credited_usdc = $2, credited_reais = $3, credited_at = NOW() WHERE signature = $4`,
		string(models.DepositCredited), creditedUSDC, creditedReais, signature)
	return err
}

// MarkDepositFailed parks a deposit for operator review (swap/credit failed).
func (s *Store) MarkDepositFailed(ctx context.Context, signature string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE deposits SET status = $1 WHERE signature = $2`, string(models.DepositFailed), signature)
	return err
}

// InsertWithdrawal records a validated, already-debited withdrawal request.
func (s *Store) InsertWithdrawal(ctx context.Context, w *models.Withdrawal) error {
	if w.ID == "" {
		w.ID = uuid.New().String()
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO withdrawals (id, account_id, dest_address, asset, amount_reais, amount_usdc, status)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		w.ID, w.AccountID, w.DestAddress, string(w.Asset), w.AmountReais, w.AmountUSDC, string(w.Status))
	return err
}

// ListDepositsByAccount returns an account's deposits, newest first - feeds the
// deposit page's "awaiting/credited" status list.
func (s *Store) ListDepositsByAccount(ctx context.Context, accountID string) ([]models.Deposit, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, signature, account_id, asset, raw_amount, credited_usdc, credited_reais, status, created_at, credited_at
		 FROM deposits WHERE account_id = $1 ORDER BY created_at DESC`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []models.Deposit
	for rows.Next() {
		var d models.Deposit
		if err := rows.Scan(&d.ID, &d.Signature, &d.AccountID, &d.Asset, &d.RawAmount,
			&d.CreditedUSDC, &d.CreditedReais, &d.Status, &d.CreatedAt, &d.CreditedAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// ListWithdrawalsByAccount returns an account's withdrawals, newest first.
func (s *Store) ListWithdrawalsByAccount(ctx context.Context, accountID string) ([]models.Withdrawal, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, account_id, dest_address, asset, amount_reais, amount_usdc, status, signature, created_at, settled_at
		 FROM withdrawals WHERE account_id = $1 ORDER BY created_at DESC`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []models.Withdrawal
	for rows.Next() {
		var w models.Withdrawal
		if err := rows.Scan(&w.ID, &w.AccountID, &w.DestAddress, &w.Asset, &w.AmountReais,
			&w.AmountUSDC, &w.Status, &w.Signature, &w.CreatedAt, &w.SettledAt); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// SetWithdrawalStatus transitions a withdrawal (REQUESTED -> SIGNING etc.).
func (s *Store) SetWithdrawalStatus(ctx context.Context, id string, status models.WithdrawalStatus) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE withdrawals SET status = $1 WHERE id = $2`, string(status), id)
	return err
}

// SetWithdrawalSent records the broadcast signature and SENT status.
func (s *Store) SetWithdrawalSent(ctx context.Context, id, signature string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE withdrawals SET status = $1, signature = $2 WHERE id = $3`,
		string(models.WithdrawalSent), signature, id)
	return err
}

// SetWithdrawalConfirmed marks the terminal CONFIRMED status.
func (s *Store) SetWithdrawalConfirmed(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE withdrawals SET status = $1, settled_at = NOW() WHERE id = $2`,
		string(models.WithdrawalConfirmed), id)
	return err
}

// SetWithdrawalFailed marks the terminal FAILED status (caller must refund the
// ledger debit).
func (s *Store) SetWithdrawalFailed(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE withdrawals SET status = $1, settled_at = NOW() WHERE id = $2`,
		string(models.WithdrawalFailed), id)
	return err
}
