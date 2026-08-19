package tests

import (
	"sync"
	"testing"

	"tchebet/bet-backend/service"

	"github.com/google/uuid"
)

// TestAdjustBalance_CreditAndDebit covers the happy path: a positive delta
// credits, a negative delta debits, and the returned balance matches.
func TestAdjustBalance_CreditAndDebit(t *testing.T) {
	cleanTables(t)

	user := uuid.New().String()
	syncUser(t, user, 100)

	bal, err := service.AdjustBalance(user, 250) // credit (deposit)
	if err != nil {
		t.Fatalf("credit failed: %v", err)
	}
	if bal != 350 {
		t.Fatalf("after credit want 350, got %d", bal)
	}

	bal, err = service.AdjustBalance(user, -150) // debit (withdrawal)
	if err != nil {
		t.Fatalf("debit failed: %v", err)
	}
	if bal != 200 {
		t.Fatalf("after debit want 200, got %d", bal)
	}
	if got := getBalance(t, user); got != 200 {
		t.Fatalf("persisted balance want 200, got %d", got)
	}
}

// TestAdjustBalance_OverdraftRejected makes sure a debit larger than the
// balance is rejected with ErrInsufficientBalance and writes nothing.
func TestAdjustBalance_OverdraftRejected(t *testing.T) {
	cleanTables(t)

	user := uuid.New().String()
	syncUser(t, user, 100)

	if _, err := service.AdjustBalance(user, -101); err != service.ErrInsufficientBalance {
		t.Fatalf("want ErrInsufficientBalance, got %v", err)
	}
	if got := getBalance(t, user); got != 100 {
		t.Fatalf("balance must be untouched at 100, got %d", got)
	}
}

// TestAdjustBalance_UnknownUser fails closed rather than crediting a
// nonexistent user into existence.
func TestAdjustBalance_UnknownUser(t *testing.T) {
	cleanTables(t)
	if _, err := service.AdjustBalance(uuid.New().String(), 100); err == nil {
		t.Fatal("expected error for unknown user, got nil")
	}
}

// TestAdjustBalance_ConcurrentCreditsAndBets is the reason this endpoint exists:
// a deposit credit racing concurrent bets must never lose or invent points. We
// start with 1000, fire 10 bets of 100 (drains to 0) interleaved with 5 credits
// of 200 (adds 1000), and assert the final balance is exactly the conserved sum
// regardless of interleaving. A read-modify-write via SyncUser would corrupt
// this; the FOR UPDATE row lock in AdjustBalance/PlaceBet keeps it exact.
func TestAdjustBalance_ConcurrentCreditsAndBets(t *testing.T) {
	cleanTables(t)

	user := uuid.New().String()
	syncUser(t, user, 1000)

	market, err := service.CreateMarket("Adjust Concurrency", []string{"A", "B"}, 50)
	if err != nil {
		t.Fatalf("CreateMarket failed: %v", err)
	}

	var wg sync.WaitGroup
	var mu sync.Mutex
	betsPlaced := 0

	// 10 concurrent bets of 100 (at most 10*100 = 1000 debited).
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := service.PlaceBet(user, market.ID, "A", 100); err == nil {
				mu.Lock()
				betsPlaced++
				mu.Unlock()
			}
		}()
	}
	// 5 concurrent credits of 200 (1000 credited).
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := service.AdjustBalance(user, 200); err != nil {
				t.Errorf("credit failed: %v", err)
			}
		}()
	}
	wg.Wait()

	// Conservation: start 1000 + 1000 credited - (betsPlaced*100) debited.
	want := int64(1000 + 1000 - betsPlaced*100)
	if got := getBalance(t, user); got != want {
		t.Fatalf("balance not conserved: want %d (bets placed=%d), got %d", want, betsPlaced, got)
	}
}
