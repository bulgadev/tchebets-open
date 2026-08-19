// Package config centralizes wallet-backend's runtime configuration. Every
// secret and network endpoint is env-driven so nothing sensitive is baked into
// the image. Defaults target Solana DEVNET - mainnet requires overriding the
// RPC URL, the USDC mint, and (the hard security gate) moving the master
// mnemonic behind a KMS/HSM instead of a plain env var.
package config

import (
	"fmt"
	"math"
	"os"
	"strconv"
)

type Config struct {
	Port string

	// BetBackendURL is where balances actually live - wallet-backend credits
	// and debits the internal ledger through it (never stores balances itself).
	BetBackendURL string

	// SolanaRPCURL is the JSON-RPC endpoint (devnet by default).
	SolanaRPCURL string

	// MasterMnemonic seeds every derived keypair. DEVNET: a plain env var,
	// encrypted-at-rest at the platform level. MAINNET GATE: must move to
	// KMS/HSM signing. Fail-closed - the service refuses to start without it.
	MasterMnemonic string

	// USDCMint is the SPL mint address for USDC on the target cluster.
	USDCMint string

	// DepositPollSeconds is how often the background poller re-scans watched
	// addresses for new deposits via RPC (getSignaturesForAddress). We poll
	// instead of using a webhook provider (Helius etc. are paid) - fully
	// self-contained against the free public RPC. Mainnet will want a paid RPC
	// endpoint for the rate limits, but that's a mainnet gate, not a blocker.
	DepositPollSeconds int

	// Withdrawal guardrails (micro-USDC). Above the manual-approval threshold a
	// withdrawal is parked for an operator instead of auto-signed.
	WithdrawalMinMicroUSDC     int64
	WithdrawalMaxMicroUSDC     int64
	WithdrawalManualReviewOver int64

	// FaucetMicroUSDC is the fixed devnet USDC grant the demo faucet button
	// dispenses per account (micro-USDC). DEVNET-ONLY affordance so visitors who
	// can't obtain devnet USDC can still run the deposit->bet->withdraw loop; the
	// grant is sent from treasury and credited through the normal deposit path.
	FaucetMicroUSDC int64

	// BRLPerUSDMicros is the fixed BRL-per-USD rate as fixed-point micros
	// (rate * 10^6), used to convert between the ledger unit (whole BRL) and
	// the chain unit (micro-USDC) on deposit/withdrawal. DEVNET: a plain fixed
	// value is fine (USDC has no real BRL price there). MAINNET GATE: this must
	// become a live oracle/feed, not a static env.
	BRLPerUSDMicros int64
}

// devnet USDC mint (Circle's official devnet mint).
const devnetUSDCMint = "4zMMC9srt5Ri5X14GAgXhaHii3GnPAEERYPJgZJDncDU"

// Load reads config from the environment, failing closed on anything that must
// not have a silent default (the master mnemonic).
func Load() (*Config, error) {
	cfg := &Config{
		Port:                       getEnv("PORT", "8080"),
		BetBackendURL:              getEnv("BET_BACKEND_URL", "http://bet-backend:8080"),
		SolanaRPCURL:               getEnv("SOLANA_RPC_URL", "https://api.devnet.solana.com"),
		MasterMnemonic:             os.Getenv("WALLET_MNEMONIC"),
		USDCMint:                   getEnv("USDC_MINT", devnetUSDCMint),
		DepositPollSeconds:         int(getEnvInt64("DEPOSIT_POLL_SECONDS", 15)),
		WithdrawalMinMicroUSDC:     getEnvInt64("WITHDRAWAL_MIN_MICRO_USDC", 1_000_000),      // 1 USDC
		WithdrawalMaxMicroUSDC:     getEnvInt64("WITHDRAWAL_MAX_MICRO_USDC", 10_000_000_000), // 10k USDC
		WithdrawalManualReviewOver: getEnvInt64("WITHDRAWAL_MANUAL_REVIEW_OVER", 1_000_000_000), // 1k USDC
		FaucetMicroUSDC:            getEnvInt64("FAUCET_MICRO_USDC", 25_000_000),                 // 25 USDC demo grant
		BRLPerUSDMicros:            parseRateMicros(getEnv("BRL_PER_USD", "5.00")),
	}

	if cfg.MasterMnemonic == "" {
		return nil, fmt.Errorf("WALLET_MNEMONIC is required (generate one with the mnemonic tool) - refusing to start without a master seed")
	}
	if cfg.BRLPerUSDMicros <= 0 {
		return nil, fmt.Errorf("BRL_PER_USD must be a positive decimal (e.g. 5.00)")
	}
	return cfg, nil
}

// parseRateMicros turns a decimal BRL-per-USD string ("5.00") into fixed-point
// micros (5_000_000). Returns 0 on a malformed value so Load fails closed.
func parseRateMicros(s string) int64 {
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || f <= 0 {
		return 0
	}
	return int64(math.Round(f * 1_000_000))
}

func getEnv(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return fallback
}

func getEnvInt64(key string, fallback int64) int64 {
	if v, ok := os.LookupEnv(key); ok {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			return n
		}
	}
	return fallback
}
