// Package chain is the boundary between wallet-backend's orchestration logic
// and the Solana network. Everything on-chain goes through the Client and
// Swapper interfaces, so the deposit/withdrawal service can be unit-tested with
// fakes (see service/) while the real, network-touching adapter (backed by
// gagliardetto/solana-go) is validated separately against devnet. This split
// exists precisely because signing real-money transactions is the kind of code
// you must exercise end-to-end, not just compile.
package chain

import (
	"context"

	"tchebet/wallet-backend/crypto"
)

// Unit constants. The internal ledger unit is the micro-USDC (USDC's own
// smallest unit, 6 decimals), so a USDC deposit's on-chain raw amount maps 1:1
// to a ledger credit - no conversion, no rounding. SOL is 9 decimals
// (lamports) and only ever reaches the ledger via a swap to USDC.
const (
	MicroUSDCPerUSDC   = 1_000_000     // 10^6
	LamportsPerSOL     = 1_000_000_000 // 10^9
	DefaultSlippageBps = 300           // 3%, per the SOL->USDC swap decision
)

// Client is the on-chain read/transfer surface wallet-backend needs. Transfers
// are signed by the passed keypair (a user's derived key for sweeps, or the
// treasury key for withdrawals/ATA funding).
type Client interface {
	// ReadSOLBalance returns an address's native balance in lamports.
	ReadSOLBalance(ctx context.Context, address string) (lamports uint64, err error)

	// ReadUSDCBalance returns the USDC balance (micro-USDC) held by owner's
	// USDC associated token account, or 0 if the ATA doesn't exist yet.
	ReadUSDCBalance(ctx context.Context, ownerAddress string) (microUSDC uint64, err error)

	// DeriveUSDCATA computes (no RPC) the deterministic USDC associated token
	// account address for an owner. Deposits are watched at this address.
	DeriveUSDCATA(ownerAddress string) (ata string, err error)

	// EnsureUSDCATA creates and rent-funds owner's USDC ATA if it's missing,
	// paid for by the treasury keypair. Returns the ATA address; sig is empty
	// if it already existed.
	EnsureUSDCATA(ctx context.Context, treasury crypto.Keypair, ownerAddress string) (ata string, sig string, err error)

	// TransferUSDC sends micro-USDC from `from`'s USDC ATA to a destination ATA.
	TransferUSDC(ctx context.Context, from crypto.Keypair, toATA string, microUSDC uint64) (sig string, err error)

	// TransferSOL sends lamports from `from` to a destination address.
	TransferSOL(ctx context.Context, from crypto.Keypair, toAddress string, lamports uint64) (sig string, err error)

	// ConfirmTx blocks until the transaction reaches a finalized commitment or
	// the context is done.
	ConfirmTx(ctx context.Context, sig string) error
}

// Swapper converts native SOL to USDC (Jupiter). Returns the actual micro-USDC
// received and the swap transaction signature. Implementations must reject a
// route worse than maxSlippageBps.
type Swapper interface {
	SwapSOLToUSDC(ctx context.Context, from crypto.Keypair, lamports uint64, maxSlippageBps int) (microUSDCOut uint64, sig string, err error)
}

// DetectedDeposit is one incoming transfer the scanner found on-chain, before
// it's been credited. RawAmount is in the asset's smallest unit.
type DetectedDeposit struct {
	Signature string
	Asset     string // "SOL" or "USDC" (models.Asset value)
	RawAmount uint64
}

// DepositScanner finds incoming transfers to a watched account since a cursor.
// This is the polling replacement for a webhook provider: implementations call
// getSignaturesForAddress on the base address (SOL) and USDC ATA, walking only
// signatures newer than sinceSig, and parse each for an incoming transfer.
// newCursor is the newest signature seen (persist it as the next sinceSig);
// dedup is still enforced downstream on the signature.
type DepositScanner interface {
	ScanDeposits(ctx context.Context, baseAddress, usdcATA, sinceSig string) (found []DetectedDeposit, newCursor string, err error)
}
