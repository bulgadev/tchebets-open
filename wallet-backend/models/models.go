// Package models holds wallet-backend's centralized domain objects - the same
// "define the things in one place, put the logic elsewhere" approach
// bet-backend/models uses. wallet-backend owns custody state only: which
// address belongs to which account, and the append-only deposit/withdrawal
// ledgers. It never duplicates markets/bets - balances live in bet-backend.
package models

import "time"

// Asset is a supported on-chain asset. Internally the platform settles in USDC
// (1 ledger point = 1 micro-USDC); SOL is swapped to USDC on deposit (see the
// deposit ingestion path). USDT is deferred (no canonical devnet mint) - the
// const is kept as the seam for when it's added, but nothing provisions or
// settles it yet.
type Asset string

const (
	AssetSOL  Asset = "SOL"
	AssetUSDC Asset = "USDC"
	AssetUSDT Asset = "USDT" // deferred, not wired
)

// Decimals is the on-chain smallest-unit exponent for each asset - USDC/USDT
// are 6-decimal SPL tokens, native SOL is 9-decimal (lamports).
func (a Asset) Decimals() int {
	switch a {
	case AssetSOL:
		return 9
	case AssetUSDC, AssetUSDT:
		return 6
	default:
		return 0
	}
}

// DepositStatus tracks a single detected on-chain deposit through crediting.
// The ledger is append-only and keyed on the transaction signature, so a
// replayed Helius webhook can never double-credit.
type DepositStatus string

const (
	DepositDetected DepositStatus = "DETECTED" // seen on-chain, not yet credited
	DepositSwapping DepositStatus = "SWAPPING" // SOL deposit being swapped to USDC
	DepositCredited DepositStatus = "CREDITED" // internal balance credited (terminal)
	DepositFailed   DepositStatus = "FAILED"   // swap/credit failed, needs operator review (terminal)
)

// WithdrawalStatus tracks a withdrawal request through on-chain settlement.
// The ledger debit happens BEFORE broadcast; a failed broadcast refunds it.
type WithdrawalStatus string

const (
	WithdrawalRequested WithdrawalStatus = "REQUESTED" // validated, ledger debited, not yet signed
	WithdrawalSigning   WithdrawalStatus = "SIGNING"   // being signed/broadcast from the hot wallet
	WithdrawalSent      WithdrawalStatus = "SENT"      // broadcast, awaiting confirmation
	WithdrawalConfirmed WithdrawalStatus = "CONFIRMED" // confirmed on-chain (terminal)
	WithdrawalFailed    WithdrawalStatus = "FAILED"    // failed; ledger refunded (terminal)
)

// WalletAddress links a platform account to its derived Solana keypair. The
// private key is NEVER stored - only the derivation_index, so the keypair can
// be re-derived on demand from the master mnemonic. USDCATA is the Associated
// Token Account pre-created for USDC (SOL needs no ATA; USDT deferred).
// Treasury/hot wallet is the reserved derivation index 0; user accounts start
// at index 1.
type WalletAddress struct {
	AccountID       string    `json:"account_id" db:"account_id"`
	DerivationIndex uint32    `json:"derivation_index" db:"derivation_index"`
	Address         string    `json:"address" db:"address"`
	USDCATA         string    `json:"usdc_ata" db:"usdc_ata"`
	CreatedAt       time.Time `json:"created_at" db:"created_at"`
}

// Deposit is one detected on-chain credit. RawAmount is in the asset's own
// smallest unit (lamports for SOL, micro-units for USDC/USDT); CreditedUSDC is
// the on-chain USDC value in micro-USDC (after a swap, for SOL) - the on-chain
// truth for reconciliation; CreditedReais is what actually landed on the
// internal ledger (micro-USDC converted at the configured BRL/USD rate). The
// bet-backend ledger settles in whole BRL (it has no concept of cents - see
// general-backend FormatMoney), so this rounds to whole reais. Signature is
// UNIQUE - the idempotency key.
type Deposit struct {
	ID            string        `json:"id" db:"id"`
	Signature     string        `json:"signature" db:"signature"`
	AccountID     string        `json:"account_id" db:"account_id"`
	Asset         Asset         `json:"asset" db:"asset"`
	RawAmount     int64         `json:"raw_amount" db:"raw_amount"`
	CreditedUSDC  int64         `json:"credited_usdc" db:"credited_usdc"`
	CreditedReais int64         `json:"credited_reais" db:"credited_reais"`
	Status        DepositStatus `json:"status" db:"status"`
	CreatedAt     time.Time     `json:"created_at" db:"created_at"`
	CreditedAt    *time.Time    `json:"credited_at,omitempty" db:"credited_at"`
}

// Withdrawal is a user-requested payout. AmountReais is the ledger unit debited
// from bet-backend (whole BRL); AmountUSDC is the micro-USDC actually sent
// on-chain (AmountReais converted at the configured BRL/USD rate). Asset is what
// actually gets sent on-chain (USDC, or USDT ~1:1). Signature is filled once
// broadcast.
type Withdrawal struct {
	ID          string           `json:"id" db:"id"`
	AccountID   string           `json:"account_id" db:"account_id"`
	DestAddress string           `json:"dest_address" db:"dest_address"`
	Asset       Asset            `json:"asset" db:"asset"`
	AmountReais int64            `json:"amount_reais" db:"amount_reais"`
	AmountUSDC  int64            `json:"amount_usdc" db:"amount_usdc"`
	Status      WithdrawalStatus `json:"status" db:"status"`
	Signature   *string          `json:"signature,omitempty" db:"signature"`
	CreatedAt   time.Time        `json:"created_at" db:"created_at"`
	SettledAt   *time.Time       `json:"settled_at,omitempty" db:"settled_at"`
}
