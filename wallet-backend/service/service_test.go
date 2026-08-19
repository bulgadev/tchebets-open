package service

import (
	"context"
	"errors"
	"testing"

	"tchebet/wallet-backend/betclient"
	"tchebet/wallet-backend/chain"
	"tchebet/wallet-backend/crypto"
	"tchebet/wallet-backend/models"
	"tchebet/wallet-backend/store"
)

const testMnemonic = "abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about"

// testRateMicros is BRL 5.00 per USD as fixed-point micros. At this rate the
// ledger unit (whole BRL) relates to the chain unit (micro-USDC) by /200_000:
// reais = microUSDC / 200_000, microUSDC = reais * 200_000.
const testRateMicros int64 = 5_000_000

// testFaucetMicroUSDC is the one-time demo grant used in tests: 25 USDC.
const testFaucetMicroUSDC int64 = 25_000_000

// ---- fakes ----

type fakeLedger struct {
	bal map[string]int64
}

func newFakeLedger() *fakeLedger { return &fakeLedger{bal: map[string]int64{}} }

func (f *fakeLedger) AdjustBalance(_ context.Context, id string, delta int64) (int64, error) {
	nb := f.bal[id] + delta
	if nb < 0 {
		return 0, betclient.ErrInsufficientBalance
	}
	f.bal[id] = nb
	return nb, nil
}

type fakeStore struct {
	seq         uint32
	byAccount   map[string]models.WalletAddress
	deposits    map[string]models.Deposit
	withdrawals map[string]models.Withdrawal
	cursors     map[string]string
	faucet      map[string]bool
	wid         int
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		byAccount:   map[string]models.WalletAddress{},
		deposits:    map[string]models.Deposit{},
		withdrawals: map[string]models.Withdrawal{},
		cursors:     map[string]string{},
		faucet:      map[string]bool{},
	}
}

func (s *fakeStore) ClaimFaucet(_ context.Context, id string) (bool, error) {
	if s.faucet[id] {
		return false, nil
	}
	s.faucet[id] = true
	return true, nil
}
func (s *fakeStore) ReleaseFaucet(_ context.Context, id string) error {
	delete(s.faucet, id)
	return nil
}

func (s *fakeStore) NextDerivationIndex(context.Context) (uint32, error) {
	s.seq++
	return s.seq, nil
}
func (s *fakeStore) GetAddressByAccount(_ context.Context, id string) (*models.WalletAddress, error) {
	w, ok := s.byAccount[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	return &w, nil
}
func (s *fakeStore) GetAddressByATA(_ context.Context, ata string) (*models.WalletAddress, error) {
	for _, w := range s.byAccount {
		if w.USDCATA == ata {
			return &w, nil
		}
	}
	return nil, store.ErrNotFound
}
func (s *fakeStore) GetAddressBySOL(_ context.Context, addr string) (*models.WalletAddress, error) {
	for _, w := range s.byAccount {
		if w.Address == addr {
			return &w, nil
		}
	}
	return nil, store.ErrNotFound
}
func (s *fakeStore) InsertAddress(_ context.Context, w models.WalletAddress) error {
	if _, ok := s.byAccount[w.AccountID]; ok {
		return errors.New("duplicate account")
	}
	s.byAccount[w.AccountID] = w
	return nil
}
func (s *fakeStore) ListAddresses(context.Context) ([]models.WalletAddress, error) {
	out := make([]models.WalletAddress, 0, len(s.byAccount))
	for _, w := range s.byAccount {
		out = append(out, w)
	}
	return out, nil
}
func (s *fakeStore) GetScanCursor(_ context.Context, addr string) (string, error) {
	return s.cursors[addr], nil
}
func (s *fakeStore) SetScanCursor(_ context.Context, addr, sig string) error {
	s.cursors[addr] = sig
	return nil
}
func (s *fakeStore) InsertDepositIfNew(_ context.Context, d models.Deposit) (bool, error) {
	if _, ok := s.deposits[d.Signature]; ok {
		return false, nil
	}
	s.deposits[d.Signature] = d
	return true, nil
}
func (s *fakeStore) SetDepositStatus(_ context.Context, sig string, st models.DepositStatus) error {
	d := s.deposits[sig]
	d.Status = st
	s.deposits[sig] = d
	return nil
}
func (s *fakeStore) MarkDepositCredited(_ context.Context, sig string, creditedUSDC, creditedReais int64) error {
	d := s.deposits[sig]
	d.Status = models.DepositCredited
	d.CreditedUSDC = creditedUSDC
	d.CreditedReais = creditedReais
	s.deposits[sig] = d
	return nil
}
func (s *fakeStore) MarkDepositFailed(_ context.Context, sig string) error {
	d := s.deposits[sig]
	d.Status = models.DepositFailed
	s.deposits[sig] = d
	return nil
}
func (s *fakeStore) InsertWithdrawal(_ context.Context, w *models.Withdrawal) error {
	s.wid++
	w.ID = string(rune('a'+s.wid))
	s.withdrawals[w.ID] = *w
	return nil
}
func (s *fakeStore) SetWithdrawalStatus(_ context.Context, id string, st models.WithdrawalStatus) error {
	w := s.withdrawals[id]
	w.Status = st
	s.withdrawals[id] = w
	return nil
}
func (s *fakeStore) SetWithdrawalSent(_ context.Context, id, sig string) error {
	w := s.withdrawals[id]
	w.Status = models.WithdrawalSent
	w.Signature = &sig
	s.withdrawals[id] = w
	return nil
}
func (s *fakeStore) SetWithdrawalConfirmed(_ context.Context, id string) error {
	w := s.withdrawals[id]
	w.Status = models.WithdrawalConfirmed
	s.withdrawals[id] = w
	return nil
}
func (s *fakeStore) SetWithdrawalFailed(_ context.Context, id string) error {
	w := s.withdrawals[id]
	w.Status = models.WithdrawalFailed
	s.withdrawals[id] = w
	return nil
}

type fakeChain struct {
	transferErr error
	confirmErr  error
	transfers   int
}

func (c *fakeChain) ReadSOLBalance(context.Context, string) (uint64, error)  { return 0, nil }
func (c *fakeChain) ReadUSDCBalance(context.Context, string) (uint64, error) { return 0, nil }
func (c *fakeChain) DeriveUSDCATA(owner string) (string, error)              { return owner + ":usdc", nil }
func (c *fakeChain) EnsureUSDCATA(_ context.Context, _ crypto.Keypair, owner string) (string, string, error) {
	return owner + ":usdc", "", nil
}
func (c *fakeChain) TransferUSDC(context.Context, crypto.Keypair, string, uint64) (string, error) {
	if c.transferErr != nil {
		return "", c.transferErr
	}
	c.transfers++
	return "sig-usdc", nil
}
func (c *fakeChain) TransferSOL(context.Context, crypto.Keypair, string, uint64) (string, error) {
	return "sig-sol", nil
}
func (c *fakeChain) ConfirmTx(context.Context, string) error { return c.confirmErr }

type fakeSwapper struct{ out uint64 }

func (s *fakeSwapper) SwapSOLToUSDC(context.Context, crypto.Keypair, uint64, int) (uint64, string, error) {
	return s.out, "sig-swap", nil
}

func newService(t *testing.T, ledger *fakeLedger, st *fakeStore, ch chain.Client, sw chain.Swapper) *Service {
	t.Helper()
	master, err := crypto.NewMasterWalletFromMnemonic(testMnemonic, "")
	if err != nil {
		t.Fatalf("master wallet: %v", err)
	}
	return New(master, st, ledger, ch, sw, Limits{Min: 1_000_000, Max: 10_000_000_000, ManualReviewOver: 1_000_000_000}, testRateMicros, testFaucetMicroUSDC)
}

// ---- tests ----

func TestRateConversion(t *testing.T) {
	// At BRL 5.00/USD: microUSDC / 200_000 == reais.
	cases := []struct {
		microUSDC, wantReais int64
	}{
		{5_000_000, 25},    // 5 USDC -> R$25
		{3_000_000, 15},    // 3 USDC -> R$15
		{142_000_000, 710}, // 142 USDC -> R$710
		{100_000, 1},       // rounds: 0.1 USDC * 5 = R$0.50 -> R$1 (half-up)
	}
	for _, c := range cases {
		if got := microUSDCToReais(c.microUSDC, testRateMicros); got != c.wantReais {
			t.Errorf("microUSDCToReais(%d) = %d, want %d", c.microUSDC, got, c.wantReais)
		}
	}

	// Inverse direction and a clean round-trip on whole-real amounts.
	if got := reaisToMicroUSDC(25, testRateMicros); got != 5_000_000 {
		t.Errorf("reaisToMicroUSDC(25) = %d, want 5_000_000", got)
	}
	for _, reais := range []int64{1, 15, 100, 25_000} {
		if rt := microUSDCToReais(reaisToMicroUSDC(reais, testRateMicros), testRateMicros); rt != reais {
			t.Errorf("round-trip %d reais -> %d", reais, rt)
		}
	}
}

func TestProvisionAccount_Idempotent(t *testing.T) {
	st := newFakeStore()
	svc := newService(t, newFakeLedger(), st, &fakeChain{}, &fakeSwapper{})

	a, err := svc.ProvisionAccount(context.Background(), "acct-1")
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	b, err := svc.ProvisionAccount(context.Background(), "acct-1")
	if err != nil {
		t.Fatalf("provision again: %v", err)
	}
	if a.Address != b.Address || a.DerivationIndex != b.DerivationIndex {
		t.Fatalf("idempotency broken: %+v vs %+v", a, b)
	}
	if st.seq != 1 {
		t.Fatalf("second provision allocated a new index, seq=%d", st.seq)
	}
	if a.DerivationIndex == treasuryIndex {
		t.Fatalf("user got the reserved treasury index 0")
	}
}

func TestFaucet_GrantsOncePerAccount(t *testing.T) {
	ledger := newFakeLedger()
	st := newFakeStore()
	ch := &fakeChain{}
	svc := newService(t, ledger, st, ch, &fakeSwapper{})

	// First claim: provisions, records the claim, transfers the grant from treasury.
	sig, err := svc.FaucetTestUSDC(context.Background(), "acct-1")
	if err != nil {
		t.Fatalf("faucet: %v", err)
	}
	if sig != "sig-usdc" {
		t.Fatalf("want transfer signature, got %q", sig)
	}
	if ch.transfers != 1 {
		t.Fatalf("want exactly one on-chain grant transfer, got %d", ch.transfers)
	}
	// The faucet must NOT credit the ledger itself - the deposit poller does that.
	if ledger.bal["acct-1"] != 0 {
		t.Fatalf("faucet should not credit the ledger directly, balance=%d", ledger.bal["acct-1"])
	}

	// Second claim: rejected, no second transfer.
	_, err = svc.FaucetTestUSDC(context.Background(), "acct-1")
	if !errors.Is(err, ErrFaucetAlreadyClaimed) {
		t.Fatalf("want ErrFaucetAlreadyClaimed, got %v", err)
	}
	if ch.transfers != 1 {
		t.Fatalf("second claim must not transfer again, got %d transfers", ch.transfers)
	}
}

func TestFaucet_ReleasesClaimOnTransferFailure(t *testing.T) {
	st := newFakeStore()
	ch := &fakeChain{transferErr: errors.New("rpc down")}
	svc := newService(t, newFakeLedger(), st, ch, &fakeSwapper{})

	if _, err := svc.FaucetTestUSDC(context.Background(), "acct-1"); err == nil {
		t.Fatal("expected transfer failure")
	}
	// The claim was released, so a retry (now with a working chain) succeeds.
	ch.transferErr = nil
	if _, err := svc.FaucetTestUSDC(context.Background(), "acct-1"); err != nil {
		t.Fatalf("retry after released claim should succeed: %v", err)
	}
}

func TestIngestDeposit_USDCCreditsAtRate(t *testing.T) {
	ledger := newFakeLedger()
	st := newFakeStore()
	svc := newService(t, ledger, st, &fakeChain{}, &fakeSwapper{})
	if _, err := svc.ProvisionAccount(context.Background(), "acct-1"); err != nil {
		t.Fatal(err)
	}

	// 5 USDC deposited; at BRL 5.00/USD that's R$25 on the ledger.
	err := svc.IngestDeposit(context.Background(), "acct-1", chain.DetectedDeposit{
		Signature: "sig-A", Asset: "USDC", RawAmount: 5_000_000,
	})
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if ledger.bal["acct-1"] != 25 {
		t.Fatalf("want 25 reais credited, got %d", ledger.bal["acct-1"])
	}
	if st.deposits["sig-A"].Status != models.DepositCredited {
		t.Fatalf("deposit not marked credited: %s", st.deposits["sig-A"].Status)
	}
	if st.deposits["sig-A"].CreditedUSDC != 5_000_000 {
		t.Fatalf("on-chain USDC truth not recorded: got %d", st.deposits["sig-A"].CreditedUSDC)
	}
	if st.deposits["sig-A"].CreditedReais != 25 {
		t.Fatalf("credited reais not recorded: got %d", st.deposits["sig-A"].CreditedReais)
	}
}

func TestIngestDeposit_ReplayNoDoubleCredit(t *testing.T) {
	ledger := newFakeLedger()
	st := newFakeStore()
	svc := newService(t, ledger, st, &fakeChain{}, &fakeSwapper{})
	if _, err := svc.ProvisionAccount(context.Background(), "acct-1"); err != nil {
		t.Fatal(err)
	}

	// 3 USDC at BRL 5.00/USD = R$15, credited exactly once across replays.
	d := chain.DetectedDeposit{Signature: "sig-dup", Asset: "USDC", RawAmount: 3_000_000}
	for i := 0; i < 3; i++ {
		if err := svc.IngestDeposit(context.Background(), "acct-1", d); err != nil {
			t.Fatalf("ingest %d: %v", i, err)
		}
	}
	if ledger.bal["acct-1"] != 15 {
		t.Fatalf("replay double-credited: balance %d, want 15", ledger.bal["acct-1"])
	}
}

func TestIngestDeposit_SOLSwapsThenCredits(t *testing.T) {
	ledger := newFakeLedger()
	st := newFakeStore()
	// 1 SOL in, swap yields 142 USDC (arbitrary); at BRL 5.00/USD that's R$710.
	svc := newService(t, ledger, st, &fakeChain{}, &fakeSwapper{out: 142_000_000})
	if _, err := svc.ProvisionAccount(context.Background(), "acct-1"); err != nil {
		t.Fatal(err)
	}

	err := svc.IngestDeposit(context.Background(), "acct-1", chain.DetectedDeposit{
		Signature: "sig-sol", Asset: "SOL", RawAmount: 1_000_000_000,
	})
	if err != nil {
		t.Fatalf("ingest SOL: %v", err)
	}
	if ledger.bal["acct-1"] != 710 {
		t.Fatalf("want swap output R$710 credited, got %d", ledger.bal["acct-1"])
	}
}

func TestRequestWithdrawal_HappyPath(t *testing.T) {
	ledger := newFakeLedger()
	ledger.bal["acct-1"] = 2500 // R$2500 = $500
	st := newFakeStore()
	ch := &fakeChain{}
	svc := newService(t, ledger, st, ch, &fakeSwapper{})
	if _, err := svc.ProvisionAccount(context.Background(), "acct-1"); err != nil {
		t.Fatal(err)
	}

	// Withdraw R$1000 = 200 USDC on-chain (within limits, below manual review).
	w, err := svc.RequestWithdrawal(context.Background(), "acct-1", "DestAddr111", 1000)
	if err != nil {
		t.Fatalf("withdraw: %v", err)
	}
	if ledger.bal["acct-1"] != 1500 {
		t.Fatalf("balance after withdraw want 1500 reais, got %d", ledger.bal["acct-1"])
	}
	if st.withdrawals[w.ID].AmountUSDC != 200_000_000 {
		t.Fatalf("on-chain amount want 200_000_000 micro-USDC, got %d", st.withdrawals[w.ID].AmountUSDC)
	}
	if ch.transfers != 1 {
		t.Fatalf("expected exactly one on-chain transfer, got %d", ch.transfers)
	}
	if st.withdrawals[w.ID].Status != models.WithdrawalConfirmed {
		t.Fatalf("withdrawal not confirmed: %s", st.withdrawals[w.ID].Status)
	}
}

func TestRequestWithdrawal_InsufficientBalance(t *testing.T) {
	ledger := newFakeLedger()
	ledger.bal["acct-1"] = 25 // R$25 = $5
	st := newFakeStore()
	ch := &fakeChain{}
	svc := newService(t, ledger, st, ch, &fakeSwapper{})
	svc.ProvisionAccount(context.Background(), "acct-1")

	// Withdraw R$1000 (200 USDC) - passes limits but overdraws the R$25 balance.
	_, err := svc.RequestWithdrawal(context.Background(), "acct-1", "DestAddr111", 1000)
	if !errors.Is(err, ErrInsufficientBalance) {
		t.Fatalf("want ErrInsufficientBalance, got %v", err)
	}
	if ledger.bal["acct-1"] != 25 {
		t.Fatalf("balance touched on failed withdraw: %d", ledger.bal["acct-1"])
	}
	if ch.transfers != 0 {
		t.Fatalf("no transfer should have happened, got %d", ch.transfers)
	}
}

func TestRequestWithdrawal_SendFailureRefunds(t *testing.T) {
	ledger := newFakeLedger()
	ledger.bal["acct-1"] = 2500 // R$2500
	st := newFakeStore()
	ch := &fakeChain{transferErr: errors.New("rpc down")}
	svc := newService(t, ledger, st, ch, &fakeSwapper{})
	svc.ProvisionAccount(context.Background(), "acct-1")

	w, err := svc.RequestWithdrawal(context.Background(), "acct-1", "DestAddr111", 1000)
	if err == nil {
		t.Fatal("expected send failure error")
	}
	if ledger.bal["acct-1"] != 2500 {
		t.Fatalf("debit not refunded after send failure: balance %d, want 2500", ledger.bal["acct-1"])
	}
	if st.withdrawals[w.ID].Status != models.WithdrawalFailed {
		t.Fatalf("withdrawal not marked failed: %s", st.withdrawals[w.ID].Status)
	}
}

func TestRequestWithdrawal_AboveManualReviewParked(t *testing.T) {
	ledger := newFakeLedger()
	ledger.bal["acct-1"] = 25_000 // R$25000 = $5000
	st := newFakeStore()
	ch := &fakeChain{}
	svc := newService(t, ledger, st, ch, &fakeSwapper{})
	svc.ProvisionAccount(context.Background(), "acct-1")

	// R$10000 = 2000 USDC > 1000 USDC manual-review threshold.
	w, err := svc.RequestWithdrawal(context.Background(), "acct-1", "DestAddr111", 10_000)
	if !errors.Is(err, ErrManualReview) {
		t.Fatalf("want ErrManualReview, got %v", err)
	}
	if ledger.bal["acct-1"] != 15_000 {
		t.Fatalf("parked withdrawal should still be debited: balance %d, want 15_000", ledger.bal["acct-1"])
	}
	if ch.transfers != 0 {
		t.Fatalf("parked withdrawal must not auto-send, got %d transfers", ch.transfers)
	}
	if st.withdrawals[w.ID].Status != models.WithdrawalRequested {
		t.Fatalf("parked withdrawal should be REQUESTED, got %s", st.withdrawals[w.ID].Status)
	}
}

func TestRequestWithdrawal_BelowMinRejected(t *testing.T) {
	ledger := newFakeLedger()
	ledger.bal["acct-1"] = 2500 // R$2500
	st := newFakeStore()
	ch := &fakeChain{}
	svc := newService(t, ledger, st, ch, &fakeSwapper{})
	svc.ProvisionAccount(context.Background(), "acct-1")

	// R$2 = 0.4 USDC on-chain, below the 1 USDC minimum.
	_, err := svc.RequestWithdrawal(context.Background(), "acct-1", "DestAddr111", 2)
	if !errors.Is(err, ErrAmountBelowMin) {
		t.Fatalf("want ErrAmountBelowMin, got %v", err)
	}
	if ledger.bal["acct-1"] != 2500 {
		t.Fatalf("balance touched on rejected withdraw: %d", ledger.bal["acct-1"])
	}
}
