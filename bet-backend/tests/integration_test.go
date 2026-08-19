package tests

import (
	"os"
	"sync"
	"testing"

	"tchebet/bet-backend/cache"
	"tchebet/bet-backend/db"
	"tchebet/bet-backend/service"

	"github.com/google/uuid"
)

// TestMain initializes database and redis for integration testing
func TestMain(m *testing.M) {
	// Set default test environments if not provided (running locally)
	if os.Getenv("DB_HOST") == "" {
		os.Setenv("DB_HOST", "localhost")
	}
	if os.Getenv("REDIS_HOST") == "" {
		os.Setenv("REDIS_HOST", "localhost:6379")
	}

	_, err := db.InitDB()
	if err != nil {
		panic("Failed to initialize test database: " + err.Error())
	}

	_, err = cache.InitRedis()
	if err != nil {
		panic("Failed to initialize test Redis: " + err.Error())
	}

	code := m.Run()
	os.Exit(code)
}

// Helper to clean tables
func cleanTables(t *testing.T) {
	_, err := db.DB.Exec("DELETE FROM bets WHERE user_id != '00000000-0000-0000-0000-000000000000'")
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.DB.Exec("DELETE FROM pools")
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.DB.Exec("DELETE FROM markets")
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.DB.Exec("DELETE FROM users WHERE id != '00000000-0000-0000-0000-000000000000'")
	if err != nil {
		t.Fatal(err)
	}
}

// Helper to sync user balance
func syncUser(t *testing.T, id string, balance int64) {
	query := `
		INSERT INTO users (id, balance, created_at)
		VALUES ($1, $2, NOW())
		ON CONFLICT (id) DO UPDATE SET balance = EXCLUDED.balance
	`
	_, err := db.DB.Exec(query, id, balance)
	if err != nil {
		t.Fatalf("failed to sync user: %v", err)
	}
}

// Helper to get user balance
func getBalance(t *testing.T, id string) int64 {
	var balance int64
	err := db.DB.QueryRow(`SELECT balance FROM users WHERE id = $1`, id).Scan(&balance)
	if err != nil {
		t.Fatalf("failed to get user balance: %v", err)
	}
	return balance
}

func TestNormalResolution(t *testing.T) {
	cleanTables(t)

	// Arrange: Users & House starting balances
	user1 := uuid.New().String()
	user2 := uuid.New().String()
	syncUser(t, user1, 1000)
	syncUser(t, user2, 1000)

	houseStart := getBalance(t, service.HouseUserID)

	// Create market with seed = 100 on outcomes A and B
	// Admin seeds A (100) and B (100). House balance should decrease by 200.
	market, err := service.CreateMarket("Who wins?", []string{"A", "B"}, 100)
	if err != nil {
		t.Fatalf("CreateMarket failed: %v", err)
	}

	// Verify house debited for seeding
	houseAfterSeed := getBalance(t, service.HouseUserID)
	if houseStart-houseAfterSeed != 200 {
		t.Errorf("Expected house to be debited 200 for seeds, got diff: %d", houseStart-houseAfterSeed)
	}

	// Act: Place bets
	_, err = service.PlaceBet(user1, market.ID, "A", 500)
	if err != nil {
		t.Fatalf("PlaceBet user1 failed: %v", err)
	}
	_, err = service.PlaceBet(user2, market.ID, "B", 200)
	if err != nil {
		t.Fatalf("PlaceBet user2 failed: %v", err)
	}

	// Lock Market
	err = service.LockMarket(market.ID)
	if err != nil {
		t.Fatalf("LockMarket failed: %v", err)
	}

	// Resolve Market (winning outcome is A)
	err = service.ResolveMarket(market.ID, "A")
	if err != nil {
		t.Fatalf("ResolveMarket failed: %v", err)
	}

	// Assert balance and conservation
	// Math verification:
	// Total pool = 500 (user1) + 200 (user2) + 100 (seed A) + 100 (seed B) = 900
	// Winning pool (A) = 600
	// Commission (3%) = 900 * 3 / 100 = 27
	// Distributable pool = 900 - 27 = 873
	// User1 payout = floor(500 * 873 / 600) = 727
	// House seed A payout = floor(100 * 873 / 600) = 145
	// Sum payouts = 727 + 145 = 872
	// Leftover Remainder = 873 - 872 = 1
	// House receives: commission (27) + remainder (1) + House seed payout (145) = 173
	// User1 final balance: 1000 - 500 + 727 = 1227
	// User2 final balance: 1000 - 200 = 800
	// House net balance change: - 200 (seed) + 173 (gain) = -27

	user1Bal := getBalance(t, user1)
	user2Bal := getBalance(t, user2)
	houseBal := getBalance(t, service.HouseUserID)

	if user1Bal != 1227 {
		t.Errorf("Expected User1 balance to be 1227, got %d", user1Bal)
	}
	if user2Bal != 800 {
		t.Errorf("Expected User2 balance to be 800, got %d", user2Bal)
	}

	houseDiff := houseBal - houseStart
	if houseDiff != -27 {
		t.Errorf("Expected House net change to be -27, got %d", houseDiff)
	}

	// Sum of user + house balance changes must equal 0
	totalChange := (user1Bal - 1000) + (user2Bal - 1000) + houseDiff
	if totalChange != 0 {
		t.Errorf("Expected total balance change to be 0, got %d", totalChange)
	}
}

func TestCancellation(t *testing.T) {
	cleanTables(t)

	user1 := uuid.New().String()
	user2 := uuid.New().String()
	syncUser(t, user1, 1000)
	syncUser(t, user2, 1000)

	houseStart := getBalance(t, service.HouseUserID)

	market, err := service.CreateMarket("Will it rain?", []string{"Yes", "No"}, 50)
	if err != nil {
		t.Fatalf("CreateMarket failed: %v", err)
	}

	_, err = service.PlaceBet(user1, market.ID, "Yes", 300)
	if err != nil {
		t.Fatalf("PlaceBet user1 failed: %v", err)
	}
	_, err = service.PlaceBet(user2, market.ID, "No", 400)
	if err != nil {
		t.Fatalf("PlaceBet user2 failed: %v", err)
	}

	// Cancel Market
	err = service.CancelMarket(market.ID)
	if err != nil {
		t.Fatalf("CancelMarket failed: %v", err)
	}

	// Assert everyone got refunded to original values
	user1Bal := getBalance(t, user1)
	user2Bal := getBalance(t, user2)
	houseBal := getBalance(t, service.HouseUserID)

	if user1Bal != 1000 {
		t.Errorf("Expected User1 refunded to 1000, got %d", user1Bal)
	}
	if user2Bal != 1000 {
		t.Errorf("Expected User2 refunded to 1000, got %d", user2Bal)
	}
	if houseBal != houseStart {
		t.Errorf("Expected House balance refunded to %d, got %d", houseStart, houseBal)
	}
}

func TestInsufficientBalance(t *testing.T) {
	cleanTables(t)

	user1 := uuid.New().String()
	syncUser(t, user1, 100)

	market, err := service.CreateMarket("Test Market", []string{"A", "B"}, 50)
	if err != nil {
		t.Fatalf("CreateMarket failed: %v", err)
	}

	// Bet 101 when balance is 100
	_, err = service.PlaceBet(user1, market.ID, "A", 101)
	if err == nil {
		t.Fatalf("Expected error for insufficient balance, got nil")
	}

	// Assert balance is unchanged
	bal := getBalance(t, user1)
	if bal != 100 {
		t.Errorf("Expected balance to remain 100, got %d", bal)
	}

	// Assert pool is unchanged
	details, err := service.GetMarketDetails(market.ID)
	if err != nil {
		t.Fatal(err)
	}
	if details.Pools["A"] != 50 {
		t.Errorf("Expected pool A to remain 50, got %d", details.Pools["A"])
	}
}

func TestIdempotency(t *testing.T) {
	cleanTables(t)

	market, err := service.CreateMarket("Idempotency Test", []string{"A", "B"}, 10)
	if err != nil {
		t.Fatalf("CreateMarket failed: %v", err)
	}

	err = service.LockMarket(market.ID)
	if err != nil {
		t.Fatal(err)
	}

	// Resolve market
	err = service.ResolveMarket(market.ID, "A")
	if err != nil {
		t.Fatalf("First ResolveMarket failed: %v", err)
	}

	// Try resolving again - should fail
	err = service.ResolveMarket(market.ID, "A")
	if err == nil {
		t.Fatalf("Expected error when resolving already resolved market, got nil")
	}

	// Try resolving with B - should fail
	err = service.ResolveMarket(market.ID, "B")
	if err == nil {
		t.Fatalf("Expected error when resolving already resolved market, got nil")
	}

	// Try cancelling resolved market - should fail
	err = service.CancelMarket(market.ID)
	if err == nil {
		t.Fatalf("Expected error when cancelling resolved market, got nil")
	}
}

func TestConcurrency(t *testing.T) {
	cleanTables(t)

	user1 := uuid.New().String()
	syncUser(t, user1, 500)

	market, err := service.CreateMarket("Concurrency Test", []string{"A", "B"}, 50)
	if err != nil {
		t.Fatalf("CreateMarket failed: %v", err)
	}

	// Fire 10 concurrent goroutines, each trying to bet 100 (total 1000 requested)
	// Since user balance is 500, exactly 5 bets must succeed and 5 must fail.
	var wg sync.WaitGroup
	successCount := 0
	failCount := 0
	var mu sync.Mutex

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := service.PlaceBet(user1, market.ID, "A", 100)
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				successCount++
			} else {
				failCount++
			}
		}()
	}

	wg.Wait()

	if successCount != 5 {
		t.Errorf("Expected exactly 5 successful bets, got %d", successCount)
	}
	if failCount != 5 {
		t.Errorf("Expected exactly 5 failed bets, got %d", failCount)
	}

	// Verify balance is exactly 0
	bal := getBalance(t, user1)
	if bal != 0 {
		t.Errorf("Expected user balance to be 0, got %d", bal)
	}

	// Verify pool A contains exactly 50 (seed) + 500 (bets) = 550
	details, err := service.GetMarketDetails(market.ID)
	if err != nil {
		t.Fatal(err)
	}
	if details.Pools["A"] != 550 {
		t.Errorf("Expected pool A to be 550, got %d", details.Pools["A"])
	}
}

func TestGetUser(t *testing.T) {
	cleanTables(t)

	user1 := uuid.New().String()
	syncUser(t, user1, 750)

	u, err := service.GetUser(user1)
	if err != nil {
		t.Fatalf("GetUser failed: %v", err)
	}
	if u.Balance != 750 {
		t.Errorf("Expected balance 750, got %d", u.Balance)
	}

	if _, err := service.GetUser(uuid.New().String()); err == nil {
		t.Errorf("Expected error for unknown user, got nil")
	}
}

func TestListMarkets(t *testing.T) {
	cleanTables(t)

	m1, err := service.CreateMarket("Market One", []string{"A", "B"}, 10)
	if err != nil {
		t.Fatalf("CreateMarket failed: %v", err)
	}
	m2, err := service.CreateMarket("Market Two", []string{"A", "B"}, 10)
	if err != nil {
		t.Fatalf("CreateMarket failed: %v", err)
	}

	markets, err := service.ListMarkets()
	if err != nil {
		t.Fatalf("ListMarkets failed: %v", err)
	}

	found := map[string]bool{}
	for _, m := range markets {
		found[m.ID] = true
	}
	if !found[m1.ID] || !found[m2.ID] {
		t.Errorf("Expected both created markets in ListMarkets result, got %d markets", len(markets))
	}
}

func TestListMarketBets_IncludesSeedBets(t *testing.T) {
	cleanTables(t)

	user1 := uuid.New().String()
	syncUser(t, user1, 1000)

	market, err := service.CreateMarket("Bet Log Test", []string{"A", "B"}, 50)
	if err != nil {
		t.Fatalf("CreateMarket failed: %v", err)
	}
	_, err = service.PlaceBet(user1, market.ID, "A", 200)
	if err != nil {
		t.Fatalf("PlaceBet failed: %v", err)
	}

	bets, err := service.ListMarketBets(market.ID)
	if err != nil {
		t.Fatalf("ListMarketBets failed: %v", err)
	}

	// 2 seed bets (A, B) from the house + 1 real bet from user1
	if len(bets) != 3 {
		t.Fatalf("Expected 3 bets (2 seed + 1 real), got %d", len(bets))
	}
	// Oldest first: seed bets were inserted before the real one
	if bets[len(bets)-1].UserID != user1 || bets[len(bets)-1].Stake != 200 {
		t.Errorf("Expected the most recent bet to be user1's 200 stake, got %+v", bets[len(bets)-1])
	}
}

func TestListUserBets(t *testing.T) {
	cleanTables(t)

	user1 := uuid.New().String()
	syncUser(t, user1, 1000)

	m1, err := service.CreateMarket("User Bets Market 1", []string{"A", "B"}, 10)
	if err != nil {
		t.Fatalf("CreateMarket failed: %v", err)
	}
	m2, err := service.CreateMarket("User Bets Market 2", []string{"A", "B"}, 10)
	if err != nil {
		t.Fatalf("CreateMarket failed: %v", err)
	}

	if _, err := service.PlaceBet(user1, m1.ID, "A", 100); err != nil {
		t.Fatalf("PlaceBet failed: %v", err)
	}
	if _, err := service.PlaceBet(user1, m2.ID, "B", 150); err != nil {
		t.Fatalf("PlaceBet failed: %v", err)
	}

	bets, err := service.ListUserBets(user1)
	if err != nil {
		t.Fatalf("ListUserBets failed: %v", err)
	}
	if len(bets) != 2 {
		t.Fatalf("Expected 2 bets for user1, got %d", len(bets))
	}
	// Newest first
	if bets[0].MarketID != m2.ID || bets[0].Stake != 150 {
		t.Errorf("Expected newest bet first (market 2, stake 150), got %+v", bets[0])
	}
}

func TestDivisionByZero(t *testing.T) {
	cleanTables(t)

	houseStart := getBalance(t, service.HouseUserID)

	// Create market with seed = 50, but no users bet on it
	market, err := service.CreateMarket("Zero Users Test", []string{"A", "B"}, 50)
	if err != nil {
		t.Fatalf("CreateMarket failed: %v", err)
	}

	err = service.LockMarket(market.ID)
	if err != nil {
		t.Fatal(err)
	}

	// Resolve market. It should complete without division by zero.
	err = service.ResolveMarket(market.ID, "A")
	if err != nil {
		t.Fatalf("ResolveMarket failed: %v", err)
	}

	// Math verification:
	// Total pool = 100 (50 A, 50 B seeds)
	// Winning pool (A) = 50
	// Commission (3%) = 100 * 3 / 100 = 3
	// Distributable pool = 100 - 3 = 97
	// Winning bets: only house (stake 50)
	// House payout = floor(50 * 97 / 50) = 97
	// Remainder = 97 - 97 = 0
	// House receives: commission (3) + remainder (0) + payout (97) = 100
	// Since house starting balance was debited 100 for seed creation,
	// the final house balance should be exactly equal to houseStart (net change = 0).

	houseBal := getBalance(t, service.HouseUserID)
	if houseBal != houseStart {
		t.Errorf("Expected house balance to equal houseStart (%d), got %d (diff %d)", houseStart, houseBal, houseBal-houseStart)
	}
}
