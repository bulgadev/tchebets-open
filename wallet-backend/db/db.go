// Package db is wallet-backend's persistence layer. It follows the same
// self-provisioning pattern general-backend/store uses: a third logical
// database ("tchebet_wallet") on the SAME postgres server/user the other two
// services already share, created on connect if missing. It holds custody
// state only - address<->account mapping and the append-only deposit/withdrawal
// ledgers. Balances are never stored here; they live in bet-backend.
package db

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

var DB *sql.DB

// InitDB ensures the wallet database exists, connects with the same
// retry-until-ready loop the other services use, and runs idempotent
// migrations.
func InitDB() (*sql.DB, error) {
	host := getEnv("DB_HOST", "localhost")
	port := getEnv("DB_PORT", "5432")
	user := getEnv("DB_USER", "bulga")
	password := getEnv("DB_PASSWORD", "waaglub4242@")
	dbname := getEnv("DB_NAME", "tchebet_wallet")

	if err := ensureDatabaseExists(host, port, user, password, dbname); err != nil {
		return nil, fmt.Errorf("failed to ensure database exists: %w", err)
	}

	dsn := fmt.Sprintf("postgres://%s:%s@%s:%s/%s?sslmode=disable", user, password, host, port, dbname)

	var db *sql.DB
	var err error
	for i := 1; i <= 10; i++ {
		db, err = sql.Open("pgx", dsn)
		if err == nil {
			err = db.Ping()
			if err == nil {
				break
			}
		}
		log.Printf("Waiting for wallet-backend database connection (attempt %d/10)... error: %v", i, err)
		time.Sleep(2 * time.Second)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to connect to database: %w", err)
	}

	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(25)
	db.SetConnMaxLifetime(5 * time.Minute)

	DB = db

	if err := runMigrations(db); err != nil {
		return nil, fmt.Errorf("failed to run database migrations: %w", err)
	}
	return db, nil
}

// ensureDatabaseExists mirrors general-backend/store: connect to the "postgres"
// maintenance DB and CREATE DATABASE if ours isn't there (postgres has no
// CREATE DATABASE IF NOT EXISTS).
func ensureDatabaseExists(host, port, user, password, dbname string) error {
	maintenanceDSN := fmt.Sprintf("postgres://%s:%s@%s:%s/postgres?sslmode=disable", user, password, host, port)

	var conn *sql.DB
	var err error
	for i := 1; i <= 10; i++ {
		conn, err = sql.Open("pgx", maintenanceDSN)
		if err == nil {
			err = conn.Ping()
			if err == nil {
				break
			}
		}
		log.Printf("Waiting for postgres server (attempt %d/10)... error: %v", i, err)
		time.Sleep(2 * time.Second)
	}
	if err != nil {
		return fmt.Errorf("failed to connect to maintenance database: %w", err)
	}
	defer conn.Close()

	var exists bool
	if err := conn.QueryRow(`SELECT EXISTS(SELECT 1 FROM pg_database WHERE datname = $1)`, dbname).Scan(&exists); err != nil {
		return fmt.Errorf("failed to check for existing database: %w", err)
	}
	if exists {
		return nil
	}
	// dbname comes from our own env var, not user input.
	if _, err := conn.Exec(fmt.Sprintf(`CREATE DATABASE %q`, dbname)); err != nil {
		return fmt.Errorf("failed to create database %q: %w", dbname, err)
	}
	log.Printf("Created database %q", dbname)
	return nil
}

func runMigrations(db *sql.DB) error {
	queries := []string{
		// Sequence that hands out stable, unique derivation indices for the
		// m/44'/501'/index'/0' path - one per provisioned account.
		`CREATE SEQUENCE IF NOT EXISTS wallet_derivation_index_seq AS BIGINT START WITH 1 MINVALUE 1;`,
		// account_id is general-backend's account UUID (== bet-backend user id).
		// The private key is never stored: derivation_index re-derives it from
		// the master mnemonic on demand.
		`CREATE TABLE IF NOT EXISTS wallet_addresses (
			account_id UUID PRIMARY KEY,
			derivation_index BIGINT NOT NULL UNIQUE,
			address TEXT NOT NULL UNIQUE,
			usdc_ata TEXT NOT NULL,
			created_at TIMESTAMP NOT NULL DEFAULT NOW()
		);`,
		// Append-only deposit ledger. signature UNIQUE is the idempotency guard:
		// a replayed/retried Helius webhook for the same tx can never
		// double-credit (INSERT hits the unique constraint).
		`CREATE TABLE IF NOT EXISTS deposits (
			id UUID PRIMARY KEY,
			signature TEXT NOT NULL UNIQUE,
			account_id UUID NOT NULL REFERENCES wallet_addresses(account_id),
			asset VARCHAR(8) NOT NULL CHECK (asset IN ('SOL','USDC')),
			raw_amount BIGINT NOT NULL CHECK (raw_amount > 0),
			credited_usdc BIGINT NOT NULL DEFAULT 0 CHECK (credited_usdc >= 0),
			status VARCHAR(16) NOT NULL CHECK (status IN ('DETECTED','SWAPPING','CREDITED','FAILED')),
			created_at TIMESTAMP NOT NULL DEFAULT NOW(),
			credited_at TIMESTAMP NULL
		);`,
		`CREATE INDEX IF NOT EXISTS idx_deposits_account ON deposits(account_id);`,
		// credited_reais is what actually hit the internal ledger (whole BRL) -
		// credited_usdc stays the on-chain USDC truth. Additive so an existing
		// tchebet_wallet DB migrates in place.
		`ALTER TABLE deposits ADD COLUMN IF NOT EXISTS credited_reais BIGINT NOT NULL DEFAULT 0 CHECK (credited_reais >= 0);`,
		// Per-address poll cursor: the last on-chain signature the deposit poller
		// has already scanned, so each pass only fetches transactions newer than
		// this instead of re-walking full history. Deposits are still deduped on
		// deposits.signature, so a lost/reset cursor is safe (just re-scans).
		`CREATE TABLE IF NOT EXISTS deposit_cursors (
			address TEXT PRIMARY KEY,
			last_sig TEXT NOT NULL,
			updated_at TIMESTAMP NOT NULL DEFAULT NOW()
		);`,
		// Withdrawal ledger. amount_usdc is micro-USDC debited from bet-backend;
		// asset is what actually gets sent on-chain.
		`CREATE TABLE IF NOT EXISTS withdrawals (
			id UUID PRIMARY KEY,
			account_id UUID NOT NULL REFERENCES wallet_addresses(account_id),
			dest_address TEXT NOT NULL,
			asset VARCHAR(8) NOT NULL CHECK (asset IN ('USDC')),
			amount_usdc BIGINT NOT NULL CHECK (amount_usdc > 0),
			status VARCHAR(16) NOT NULL CHECK (status IN ('REQUESTED','SIGNING','SENT','CONFIRMED','FAILED')),
			signature TEXT NULL,
			created_at TIMESTAMP NOT NULL DEFAULT NOW(),
			settled_at TIMESTAMP NULL
		);`,
		`CREATE INDEX IF NOT EXISTS idx_withdrawals_account ON withdrawals(account_id);`,
		// amount_reais is the ledger unit debited from bet-backend (whole BRL);
		// amount_usdc stays the micro-USDC actually sent on-chain. Additive migration.
		`ALTER TABLE withdrawals ADD COLUMN IF NOT EXISTS amount_reais BIGINT NOT NULL DEFAULT 0 CHECK (amount_reais >= 0);`,
		// Demo faucet claims: one row per account that has drawn its one-time
		// devnet USDC grant. The account_id PRIMARY KEY is the anti-drain guard -
		// a second faucet request for the same account hits the conflict and is
		// rejected (see store.ClaimFaucet). Devnet-only demo affordance.
		`CREATE TABLE IF NOT EXISTS faucet_claims (
			account_id UUID PRIMARY KEY REFERENCES wallet_addresses(account_id),
			claimed_at TIMESTAMP NOT NULL DEFAULT NOW()
		);`,
	}

	for _, query := range queries {
		if _, err := db.Exec(query); err != nil {
			return fmt.Errorf("migration query failed: %s, error: %w", query, err)
		}
	}
	log.Println("wallet-backend database migrations executed successfully.")
	return nil
}

func getEnv(key, fallback string) string {
	if value, exists := os.LookupEnv(key); exists {
		return value
	}
	return fallback
}
