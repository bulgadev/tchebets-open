// Package store is general-backend's own persistence layer - Phase 1/1.2 had
// none of this on purpose (pure sanitizing proxy), but Phase 2 needs
// somewhere to keep accounts (email/password/admin flag), which don't belong
// on bet-backend's balance-only users table.
package store

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

var DB *sql.DB

// InitDB connects to Postgres and runs schema setup, mirroring
// bet-backend/db/db.go's retry-connect + idempotent CREATE TABLE IF NOT
// EXISTS pattern. The one wrinkle bet-backend doesn't have: this is a
// second logical database on the *same* postgres container/user
// bet-backend already uses, and a stock postgres image only seeds one DB
// (via POSTGRES_DB) on first boot - so before connecting to our own DB we
// first connect to the server's default "postgres" maintenance database and
// create ours if it's missing. Self-heals the same way bet-backend's
// migrations already do after a volume reset.
func InitDB() (*sql.DB, error) {
	host := getEnv("DB_HOST", "localhost")
	port := getEnv("DB_PORT", "5432")
	user := getEnv("DB_USER", "bulga")
	password := getEnv("DB_PASSWORD", "waaglub4242@")
	dbname := getEnv("DB_NAME", "tchebet_general")

	if err := ensureDatabaseExists(host, port, user, password, dbname); err != nil {
		return nil, fmt.Errorf("failed to ensure database exists: %w", err)
	}

	dsn := fmt.Sprintf("postgres://%s:%s@%s:%s/%s?sslmode=disable", user, password, host, port, dbname)

	var db *sql.DB
	var err error

	// Retry connection since the DB container might take a few seconds to accept connections
	for i := 1; i <= 10; i++ {
		db, err = sql.Open("pgx", dsn)
		if err == nil {
			err = db.Ping()
			if err == nil {
				break
			}
		}
		log.Printf("Waiting for general-backend database connection (attempt %d/10)... error: %v", i, err)
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

// ensureDatabaseExists connects to the server's default "postgres" database
// and creates dbname if it's not there yet. Postgres has no CREATE DATABASE
// IF NOT EXISTS, so check pg_database first.
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

	// Can't parameterize a database name in CREATE DATABASE - dbname comes from
	// our own env var, not user input, so this is safe.
	if _, err := conn.Exec(fmt.Sprintf(`CREATE DATABASE %q`, dbname)); err != nil {
		return fmt.Errorf("failed to create database %q: %w", dbname, err)
	}
	log.Printf("Created database %q", dbname)
	return nil
}

func runMigrations(db *sql.DB) error {
	queries := []string{
		`CREATE TABLE IF NOT EXISTS accounts (
			id UUID PRIMARY KEY,
			email TEXT NOT NULL UNIQUE,
			password_hash TEXT NOT NULL,
			is_admin BOOLEAN NOT NULL DEFAULT FALSE,
			created_at TIMESTAMP NOT NULL DEFAULT NOW()
		);`,
	}

	for _, query := range queries {
		if _, err := db.Exec(query); err != nil {
			return fmt.Errorf("migration query failed: %s, error: %w", query, err)
		}
	}

	log.Println("general-backend database migrations executed successfully.")
	return nil
}

func getEnv(key, fallback string) string {
	if value, exists := os.LookupEnv(key); exists {
		return value
	}
	return fallback
}
