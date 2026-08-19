package db

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// Shouldnt DB be a object???
var DB *sql.DB

// InitDB establishes connection to PostgreSQL and runs schema setup migrations
func InitDB() (*sql.DB, error) {
	host := getEnv("DB_HOST", "localhost")
	port := getEnv("DB_PORT", "5432")
	user := getEnv("DB_USER", "bulga")
	password := getEnv("DB_PASSWORD", "waaglub4242@")
	dbname := getEnv("DB_NAME", "tchebet_betting")

	dsn := fmt.Sprintf("postgres://%s:%s@%s:%s/%s?sslmode=disable", user, password, host, port, dbname)

	var db *sql.DB
	var err error

	// Retry connection since DB container might take a few seconds to accept connections
	for i := 1; i <= 10; i++ {
		db, err = sql.Open("pgx", dsn)
		if err == nil {
			err = db.Ping()
			if err == nil {
				break
			}
		}
		log.Printf("Waiting for database connection (attempt %d/10)... error: %v", i, err)
		time.Sleep(2 * time.Second)
	}

	if err != nil {
		return nil, fmt.Errorf("failed to connect to database: %w", err)
	}

	// Set connection pool limits
	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(25)
	db.SetConnMaxLifetime(5 * time.Minute)

	DB = db

	if err := runMigrations(db); err != nil {
		return nil, fmt.Errorf("failed to run database migrations: %w", err)
	}

	return db, nil
}

func runMigrations(db *sql.DB) error {
	// 1. Create tables
	queries := []string{
		`CREATE TABLE IF NOT EXISTS users (
			id UUID PRIMARY KEY,
			balance BIGINT NOT NULL CHECK (balance >= 0),
			created_at TIMESTAMP NOT NULL DEFAULT NOW()
		);`,
		`CREATE TABLE IF NOT EXISTS markets (
			id UUID PRIMARY KEY,
			question TEXT NOT NULL,
			outcomes JSONB NOT NULL,
			status VARCHAR(20) NOT NULL CHECK (status IN ('OPEN', 'LOCKED', 'RESOLVED', 'CANCELLED')),
			seed BIGINT NOT NULL CHECK (seed >= 0),
			winning_outcome VARCHAR(100),
			created_at TIMESTAMP NOT NULL DEFAULT NOW()
		);`,
		`CREATE TABLE IF NOT EXISTS pools (
			market_id UUID NOT NULL REFERENCES markets(id) ON DELETE CASCADE,
			outcome VARCHAR(100) NOT NULL,
			total BIGINT NOT NULL CHECK (total >= 0),
			PRIMARY KEY (market_id, outcome)
		);`,
		`CREATE TABLE IF NOT EXISTS bets (
			id UUID PRIMARY KEY,
			market_id UUID NOT NULL REFERENCES markets(id) ON DELETE CASCADE,
			user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			outcome VARCHAR(100) NOT NULL,
			stake BIGINT NOT NULL CHECK (stake > 0),
			created_at TIMESTAMP NOT NULL DEFAULT NOW()
		);`,
		// Ensure system/house user exists
		`INSERT INTO users (id, balance, created_at)
		 VALUES ('00000000-0000-0000-0000-000000000000', 1000000000000, NOW())
		 ON CONFLICT (id) DO NOTHING;`,
		// original_user_id records who a bet was placed by, set once at insert
		// time and never touched again - independent of bets.user_id, which
		// listing_service.go reassigns on a sale. O(1) "who placed this
		// originally" without walking bet_listings.
		`ALTER TABLE bets ADD COLUMN IF NOT EXISTS original_user_id UUID REFERENCES users(id);`,
		`UPDATE bets SET original_user_id = user_id WHERE original_user_id IS NULL;`,
		`CREATE INDEX IF NOT EXISTS idx_bets_user_id ON bets(user_id);`,
		`CREATE INDEX IF NOT EXISTS idx_bets_market_id ON bets(market_id);`,
		// bet_listings is the phase-3 secondary market: a seller lists an
		// existing bet for sale at a fixed price, a buyer takes it as-is (no
		// order book). Selling reassigns bets.user_id (see
		// service/listing_service.go) - this table is the append-only audit
		// trail of who sold what to whom, never edited after SOLD/CANCELLED.
		`CREATE TABLE IF NOT EXISTS bet_listings (
			id UUID PRIMARY KEY,
			bet_id UUID NOT NULL REFERENCES bets(id) ON DELETE CASCADE,
			market_id UUID NOT NULL REFERENCES markets(id) ON DELETE CASCADE,
			seller_id UUID NOT NULL REFERENCES users(id),
			buyer_id UUID NULL REFERENCES users(id),
			asking_price BIGINT NOT NULL CHECK (asking_price > 0),
			status VARCHAR(20) NOT NULL CHECK (status IN ('LISTED', 'SOLD', 'CANCELLED')) DEFAULT 'LISTED',
			created_at TIMESTAMP NOT NULL DEFAULT NOW(),
			sold_at TIMESTAMP NULL,
			cancelled_at TIMESTAMP NULL
		);`,
		// Only one active listing per bet at a time - the DB-level guard
		// against double-listing (a second concurrent INSERT while one is
		// already LISTED hits this and errors with a unique_violation).
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_bet_listings_one_active_per_bet
		 ON bet_listings(bet_id) WHERE status = 'LISTED';`,
		`CREATE INDEX IF NOT EXISTS idx_bet_listings_market_status ON bet_listings(market_id, status);`,
		`CREATE INDEX IF NOT EXISTS idx_bet_listings_seller_status ON bet_listings(seller_id, status);`,
	}

	for _, query := range queries {
		if _, err := db.Exec(query); err != nil {
			return fmt.Errorf("migration query failed: %s, error: %w", query, err)
		}
	}

	log.Println("Database migrations executed successfully.")
	return nil
}

func getEnv(key, fallback string) string {
	if value, exists := os.LookupEnv(key); exists {
		return value
	}
	return fallback
}
