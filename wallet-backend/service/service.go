// Package service is wallet-backend's orchestration: it wires the master
// wallet, the on-chain adapter, the swap adapter, the store, and bet-backend's
// balance ledger into the three real flows - provision an account's address,
// ingest a detected deposit, and process a withdrawal. Everything on-chain and
// the ledger are behind interfaces (chain.Client/Swapper/DepositScanner,
// betclient.LedgerClient, Persistence) so this logic - the part that must not
// lose or invent money - is unit-tested with fakes, no network.
package service

import (
	"context"
	"errors"
	"fmt"
	"math/big"

	"tchebet/wallet-backend/betclient"
	"tchebet/wallet-backend/chain"
	"tchebet/wallet-backend/crypto"
	"tchebet/wallet-backend/models"
	"tchebet/wallet-backend/store"
)

// The internal ledger settles in whole BRL (bet-backend's unit has no cents -
// see general-backend FormatMoney); the chain settles in micro-USDC (10^6/USDC).
// These convert between the two at a fixed-point BRL/USD rate expressed in micros
// (rateMicros = BRL-per-USD * 10^6), rounding half-up. All inputs are
// non-negative in this service.
//
//	reais     = microUSDC * rate / 10^6   (USDC = micro/10^6; BRL = USDC*rate)
//	microUSDC = reais     * 10^6 / rate
//
// With rate carried as micros the divisors become 10^12 and rate-micros.
func microUSDCToReais(microUSDC, rateMicros int64) int64 {
	return mulDivRound(microUSDC, rateMicros, 1_000_000_000_000)
}

func reaisToMicroUSDC(reais, rateMicros int64) int64 {
	return mulDivRound(reais, 1_000_000_000_000, rateMicros)
}

// mulDivRound computes round(a*b/d) with big.Int so the intermediate product
// can't overflow int64 at large amounts. Half-up rounding (valid for a,b,d >= 0).
func mulDivRound(a, b, d int64) int64 {
	x := new(big.Int).Mul(big.NewInt(a), big.NewInt(b))
	x.Add(x, big.NewInt(d/2))
	x.Div(x, big.NewInt(d))
	return x.Int64()
}

// treasuryIndex is the reserved derivation index for the platform hot/treasury
// wallet: it funds ATA rent and signs withdrawals. User accounts are handed
// indices from the sequence, which starts at 1, so they never collide with it.
const treasuryIndex uint32 = 0

// Persistence is the store surface the service needs (store.Store satisfies it;
// tests fake it).
type Persistence interface {
	NextDerivationIndex(ctx context.Context) (uint32, error)
	GetAddressByAccount(ctx context.Context, accountID string) (*models.WalletAddress, error)
	GetAddressByATA(ctx context.Context, ata string) (*models.WalletAddress, error)
	GetAddressBySOL(ctx context.Context, address string) (*models.WalletAddress, error)
	InsertAddress(ctx context.Context, w models.WalletAddress) error
	ListAddresses(ctx context.Context) ([]models.WalletAddress, error)
	GetScanCursor(ctx context.Context, address string) (string, error)
	SetScanCursor(ctx context.Context, address, lastSig string) error
	ClaimFaucet(ctx context.Context, accountID string) (bool, error)
	ReleaseFaucet(ctx context.Context, accountID string) error
	InsertDepositIfNew(ctx context.Context, d models.Deposit) (bool, error)
	SetDepositStatus(ctx context.Context, signature string, status models.DepositStatus) error
	MarkDepositCredited(ctx context.Context, signature string, creditedUSDC, creditedReais int64) error
	MarkDepositFailed(ctx context.Context, signature string) error
	InsertWithdrawal(ctx context.Context, w *models.Withdrawal) error
	SetWithdrawalStatus(ctx context.Context, id string, status models.WithdrawalStatus) error
	SetWithdrawalSent(ctx context.Context, id, signature string) error
	SetWithdrawalConfirmed(ctx context.Context, id string) error
	SetWithdrawalFailed(ctx context.Context, id string) error
}

// Limits are the withdrawal guardrails (micro-USDC).
type Limits struct {
	Min              int64
	Max              int64
	ManualReviewOver int64
}

type Service struct {
	master     *crypto.MasterWallet
	store      Persistence
	ledger     betclient.LedgerClient
	chain      chain.Client
	swapper    chain.Swapper
	limits     Limits
	rateMicros int64 // BRL-per-USD * 10^6, the ledger<->chain conversion rate
	faucet     int64 // one-time devnet USDC grant (micro-USDC) the demo faucet sends
}

func New(master *crypto.MasterWallet, st Persistence, ledger betclient.LedgerClient, ch chain.Client, sw chain.Swapper, limits Limits, rateMicros, faucetMicroUSDC int64) *Service {
	return &Service{master: master, store: st, ledger: ledger, chain: ch, swapper: sw, limits: limits, rateMicros: rateMicros, faucet: faucetMicroUSDC}
}

// treasury returns the platform hot-wallet keypair (derivation index 0).
func (s *Service) treasury() crypto.Keypair {
	return s.master.DeriveAccount(treasuryIndex)
}

// ProvisionAccount returns the account's deposit address, creating it on first
// call. Idempotent: a second call (or a lost race) returns the existing row.
// The USDC ATA is created and rent-funded by the treasury upfront so a USDC
// deposit has somewhere to land.
func (s *Service) ProvisionAccount(ctx context.Context, accountID string) (*models.WalletAddress, error) {
	if existing, err := s.store.GetAddressByAccount(ctx, accountID); err == nil {
		return existing, nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}

	index, err := s.store.NextDerivationIndex(ctx)
	if err != nil {
		return nil, err
	}
	kp := s.master.DeriveAccount(index)

	ata, _, err := s.chain.EnsureUSDCATA(ctx, s.treasury(), kp.Address())
	if err != nil {
		return nil, fmt.Errorf("ensuring USDC ATA: %w", err)
	}

	row := models.WalletAddress{
		AccountID:       accountID,
		DerivationIndex: index,
		Address:         kp.Address(),
		USDCATA:         ata,
	}
	if err := s.store.InsertAddress(ctx, row); err != nil {
		// A concurrent provision for the same account won the PRIMARY KEY race;
		// return whatever landed rather than erroring.
		if existing, gErr := s.store.GetAddressByAccount(ctx, accountID); gErr == nil {
			return existing, nil
		}
		return nil, err
	}
	return &row, nil
}

// ErrFaucetAlreadyClaimed is returned when an account has already drawn its
// one-time demo faucet grant.
var ErrFaucetAlreadyClaimed = errors.New("faucet already claimed")

// FaucetTestUSDC sends the fixed devnet USDC demo grant from treasury to the
// account's USDC ATA, then returns the transfer signature. It deliberately does
// NOT credit the ledger here: the grant lands in the account's watched ATA and
// is credited by the normal deposit poller/IngestDeposit path, so the faucet
// exercises the real deposit loop end-to-end (just with treasury, not a user's
// Phantom, originating the transfer). One grant per account: the claim is taken
// atomically first (fail-closed against double-clicks) and released only if the
// on-chain send fails, so a genuine failure can be retried. DEVNET-ONLY.
func (s *Service) FaucetTestUSDC(ctx context.Context, accountID string) (string, error) {
	// Ensure the account has an address + USDC ATA to receive into.
	row, err := s.ProvisionAccount(ctx, accountID)
	if err != nil {
		return "", fmt.Errorf("provisioning for faucet: %w", err)
	}

	claimed, err := s.store.ClaimFaucet(ctx, accountID)
	if err != nil {
		return "", err
	}
	if !claimed {
		return "", ErrFaucetAlreadyClaimed
	}

	sig, err := s.chain.TransferUSDC(ctx, s.treasury(), row.USDCATA, uint64(s.faucet))
	if err != nil {
		// Release the claim so the user can retry a transient on-chain failure.
		_ = s.store.ReleaseFaucet(ctx, accountID)
		return "", fmt.Errorf("faucet transfer: %w", err)
	}
	return sig, nil
}

// IngestDeposit credits a single detected on-chain deposit to the internal
// ledger, idempotently. The signature UNIQUE insert is the guard: a replayed
// scan for the same transaction inserts nothing and returns early, so no double
// credit. A USDC deposit credits 1:1 (micro-USDC == ledger unit); a SOL deposit
// is swapped to USDC first and the swap output is what's credited.
//
// Crash-safety note: if the process dies after the ledger credit but before
// MarkDepositCredited, the deposit is stuck in DETECTED/SWAPPING (already
// credited). A replay is still deduped (no double credit) - the safe direction.
// Recovering a stuck-but-credited deposit is an operator/reconciliation task,
// flagged for mainnet hardening; on devnet the window is negligible.
func (s *Service) IngestDeposit(ctx context.Context, accountID string, d chain.DetectedDeposit) error {
	asset := models.Asset(d.Asset)
	dep := models.Deposit{
		Signature: d.Signature,
		AccountID: accountID,
		Asset:     asset,
		RawAmount: int64(d.RawAmount),
		Status:    models.DepositDetected,
	}
	inserted, err := s.store.InsertDepositIfNew(ctx, dep)
	if err != nil {
		return err
	}
	if !inserted {
		// Already seen - idempotent no-op.
		return nil
	}

	creditedUSDC, err := s.resolveCreditAmount(ctx, accountID, asset, d)
	if err != nil {
		_ = s.store.MarkDepositFailed(ctx, d.Signature)
		return err
	}
	// The ledger settles in whole BRL - convert the on-chain USDC value at the
	// configured rate before crediting; keep the micro-USDC as the on-chain truth.
	creditedReais := microUSDCToReais(creditedUSDC, s.rateMicros)

	if _, err := s.ledger.AdjustBalance(ctx, accountID, creditedReais); err != nil {
		_ = s.store.MarkDepositFailed(ctx, d.Signature)
		return fmt.Errorf("crediting ledger: %w", err)
	}
	return s.store.MarkDepositCredited(ctx, d.Signature, creditedUSDC, creditedReais)
}

// resolveCreditAmount turns an on-chain raw amount into micro-USDC to credit:
// USDC is 1:1; SOL is swapped to USDC (the account's own derived key signs the
// swap of its just-received SOL) and the swap output is credited.
func (s *Service) resolveCreditAmount(ctx context.Context, accountID string, asset models.Asset, d chain.DetectedDeposit) (int64, error) {
	switch asset {
	case models.AssetUSDC:
		return int64(d.RawAmount), nil
	case models.AssetSOL:
		if err := s.store.SetDepositStatus(ctx, d.Signature, models.DepositSwapping); err != nil {
			return 0, err
		}
		row, err := s.store.GetAddressByAccount(ctx, accountID)
		if err != nil {
			return 0, err
		}
		kp := s.master.DeriveAccount(row.DerivationIndex)
		out, _, err := s.swapper.SwapSOLToUSDC(ctx, kp, d.RawAmount, chain.DefaultSlippageBps)
		if err != nil {
			return 0, fmt.Errorf("swapping SOL->USDC: %w", err)
		}
		return int64(out), nil
	default:
		return 0, fmt.Errorf("unsupported deposit asset %q", asset)
	}
}

// Withdrawal errors surfaced to the handler for status mapping.
var (
	ErrAmountBelowMin      = errors.New("amount below minimum withdrawal")
	ErrAmountAboveMax      = errors.New("amount above maximum withdrawal")
	ErrInsufficientBalance = errors.New("insufficient balance")
	ErrManualReview        = errors.New("withdrawal requires manual review")
)

// RequestWithdrawal debits the internal ledger (in BRL centavos) and sends the
// equivalent USDC on-chain from the treasury. Order matters for real money:
// validate -> DEBIT the ledger (atomic, via bet-backend) -> record -> sign+
// broadcast -> confirm. If the on-chain send fails at any point after the debit,
// the debit is REFUNDED so the user never loses funds to a failed payout.
// Amounts over the manual-review threshold are debited and parked in REQUESTED
// for an operator instead of auto-signed. amountReais is the ledger unit the
// user asked to withdraw; guardrails apply to the micro-USDC actually sent.
func (s *Service) RequestWithdrawal(ctx context.Context, accountID, destAddress string, amountReais int64) (*models.Withdrawal, error) {
	// Convert once up front: the ledger settles in whole BRL, the payout in USDC,
	// and the guardrails (min/max/manual-review) are about the on-chain payout size.
	amountMicroUSDC := reaisToMicroUSDC(amountReais, s.rateMicros)
	if amountMicroUSDC < s.limits.Min {
		return nil, ErrAmountBelowMin
	}
	if amountMicroUSDC > s.limits.Max {
		return nil, ErrAmountAboveMax
	}

	// Atomic debit first - fails closed on insufficient balance, touching nothing.
	if _, err := s.ledger.AdjustBalance(ctx, accountID, -amountReais); err != nil {
		if errors.Is(err, betclient.ErrInsufficientBalance) {
			return nil, ErrInsufficientBalance
		}
		return nil, fmt.Errorf("debiting ledger: %w", err)
	}

	w := &models.Withdrawal{
		AccountID:   accountID,
		DestAddress: destAddress,
		Asset:       models.AssetUSDC,
		AmountReais: amountReais,
		AmountUSDC:  amountMicroUSDC,
		Status:      models.WithdrawalRequested,
	}
	if err := s.store.InsertWithdrawal(ctx, w); err != nil {
		s.refund(ctx, accountID, amountReais) // best-effort: never strand a debit
		return nil, err
	}

	// Above the threshold: leave it debited and parked for an operator.
	if amountMicroUSDC > s.limits.ManualReviewOver {
		return w, ErrManualReview
	}

	if err := s.settleWithdrawal(ctx, w); err != nil {
		return w, err
	}
	return w, nil
}

// settleWithdrawal signs and broadcasts the on-chain USDC transfer, refunding
// the ledger debit if anything fails.
func (s *Service) settleWithdrawal(ctx context.Context, w *models.Withdrawal) error {
	_ = s.store.SetWithdrawalStatus(ctx, w.ID, models.WithdrawalSigning)

	destATA, _, err := s.chain.EnsureUSDCATA(ctx, s.treasury(), w.DestAddress)
	if err != nil {
		return s.failAndRefund(ctx, w, fmt.Errorf("ensuring dest ATA: %w", err))
	}
	sig, err := s.chain.TransferUSDC(ctx, s.treasury(), destATA, uint64(w.AmountUSDC))
	if err != nil {
		return s.failAndRefund(ctx, w, fmt.Errorf("sending USDC: %w", err))
	}
	if err := s.store.SetWithdrawalSent(ctx, w.ID, sig); err != nil {
		return err // sent on-chain; do NOT refund - money left, this is a bookkeeping error to alert on
	}
	w.Signature = &sig

	if err := s.chain.ConfirmTx(ctx, sig); err != nil {
		return s.failAndRefund(ctx, w, fmt.Errorf("confirming withdrawal: %w", err))
	}
	return s.store.SetWithdrawalConfirmed(ctx, w.ID)
}

// failAndRefund marks the withdrawal FAILED and credits the debited amount back.
func (s *Service) failAndRefund(ctx context.Context, w *models.Withdrawal, cause error) error {
	_ = s.store.SetWithdrawalFailed(ctx, w.ID)
	s.refund(ctx, w.AccountID, w.AmountReais)
	return cause
}

func (s *Service) refund(ctx context.Context, accountID string, amountReais int64) {
	// Best-effort refund of a debit whose payout didn't happen. A failure here
	// is a serious alert condition (user is owed money) but there's nothing to
	// roll back to - log/surface it upstream.
	_, _ = s.ledger.AdjustBalance(ctx, accountID, amountReais)
}
