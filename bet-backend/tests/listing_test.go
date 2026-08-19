package tests

import (
	"errors"
	"sync"
	"testing"

	"tchebet/bet-backend/service"

	"github.com/google/uuid"
)

func TestListBetForSale_HappyPath(t *testing.T) {
	cleanTables(t)

	seller := uuid.New().String()
	syncUser(t, seller, 1000)

	market, err := service.CreateMarket("Sale Test", []string{"A", "B"}, 0)
	if err != nil {
		t.Fatalf("CreateMarket failed: %v", err)
	}
	bet, err := service.PlaceBet(seller, market.ID, "A", 100)
	if err != nil {
		t.Fatalf("PlaceBet failed: %v", err)
	}

	listing, err := service.ListBetForSale(bet.ID, seller, 150)
	if err != nil {
		t.Fatalf("ListBetForSale failed: %v", err)
	}
	if listing.Status != "LISTED" || listing.AskingPrice != 150 {
		t.Errorf("expected LISTED listing at price 150, got %+v", listing)
	}
}

func TestListBetForSale_RejectsNonOwner(t *testing.T) {
	cleanTables(t)

	owner := uuid.New().String()
	other := uuid.New().String()
	syncUser(t, owner, 1000)
	syncUser(t, other, 1000)

	market, err := service.CreateMarket("Non-owner Test", []string{"A", "B"}, 0)
	if err != nil {
		t.Fatalf("CreateMarket failed: %v", err)
	}
	bet, err := service.PlaceBet(owner, market.ID, "A", 100)
	if err != nil {
		t.Fatalf("PlaceBet failed: %v", err)
	}

	_, err = service.ListBetForSale(bet.ID, other, 150)
	if !errors.Is(err, service.ErrNotBetOwner) {
		t.Errorf("expected ErrNotBetOwner, got %v", err)
	}
}

func TestListBetForSale_RejectsWhenMarketLocked(t *testing.T) {
	cleanTables(t)

	seller := uuid.New().String()
	syncUser(t, seller, 1000)

	market, err := service.CreateMarket("Locked Test", []string{"A", "B"}, 0)
	if err != nil {
		t.Fatalf("CreateMarket failed: %v", err)
	}
	bet, err := service.PlaceBet(seller, market.ID, "A", 100)
	if err != nil {
		t.Fatalf("PlaceBet failed: %v", err)
	}
	if err := service.LockMarket(market.ID); err != nil {
		t.Fatalf("LockMarket failed: %v", err)
	}

	_, err = service.ListBetForSale(bet.ID, seller, 150)
	if !errors.Is(err, service.ErrMarketNotOpen) {
		t.Errorf("expected ErrMarketNotOpen, got %v", err)
	}
}

func TestListBetForSale_RejectsHouseSeedBet(t *testing.T) {
	cleanTables(t)

	market, err := service.CreateMarket("Seed Test", []string{"A", "B"}, 50)
	if err != nil {
		t.Fatalf("CreateMarket failed: %v", err)
	}

	bets, err := service.ListMarketBets(market.ID)
	if err != nil {
		t.Fatalf("ListMarketBets failed: %v", err)
	}
	if len(bets) == 0 {
		t.Fatal("expected seed bets to exist")
	}

	_, err = service.ListBetForSale(bets[0].ID, service.HouseUserID, 150)
	if !errors.Is(err, service.ErrCannotListHouseBet) {
		t.Errorf("expected ErrCannotListHouseBet, got %v", err)
	}
}

func TestListBetForSale_RejectsDoubleListing(t *testing.T) {
	cleanTables(t)

	seller := uuid.New().String()
	syncUser(t, seller, 1000)

	market, err := service.CreateMarket("Double Listing Test", []string{"A", "B"}, 0)
	if err != nil {
		t.Fatalf("CreateMarket failed: %v", err)
	}
	bet, err := service.PlaceBet(seller, market.ID, "A", 100)
	if err != nil {
		t.Fatalf("PlaceBet failed: %v", err)
	}

	if _, err := service.ListBetForSale(bet.ID, seller, 150); err != nil {
		t.Fatalf("first ListBetForSale failed: %v", err)
	}
	_, err = service.ListBetForSale(bet.ID, seller, 200)
	if !errors.Is(err, service.ErrBetAlreadyListed) {
		t.Errorf("expected ErrBetAlreadyListed, got %v", err)
	}
}

func TestCancelListing_HappyPath(t *testing.T) {
	cleanTables(t)

	seller := uuid.New().String()
	syncUser(t, seller, 1000)

	market, err := service.CreateMarket("Cancel Listing Test", []string{"A", "B"}, 0)
	if err != nil {
		t.Fatalf("CreateMarket failed: %v", err)
	}
	bet, err := service.PlaceBet(seller, market.ID, "A", 100)
	if err != nil {
		t.Fatalf("PlaceBet failed: %v", err)
	}
	listing, err := service.ListBetForSale(bet.ID, seller, 150)
	if err != nil {
		t.Fatalf("ListBetForSale failed: %v", err)
	}

	if err := service.CancelListing(listing.ID, seller); err != nil {
		t.Fatalf("CancelListing failed: %v", err)
	}

	sellerBal := getBalance(t, seller)
	if sellerBal != 900 { // 1000 - 100 staked, cancellation touches no balances
		t.Errorf("expected seller balance unchanged by cancellation (900), got %d", sellerBal)
	}

	active, err := service.ListActiveListingsForMarket(market.ID)
	if err != nil {
		t.Fatalf("ListActiveListingsForMarket failed: %v", err)
	}
	if len(active) != 0 {
		t.Errorf("expected no active listings after cancel, got %d", len(active))
	}
}

func TestCancelListing_RejectsNonOwner(t *testing.T) {
	cleanTables(t)

	seller := uuid.New().String()
	other := uuid.New().String()
	syncUser(t, seller, 1000)
	syncUser(t, other, 1000)

	market, err := service.CreateMarket("Cancel Non-owner Test", []string{"A", "B"}, 0)
	if err != nil {
		t.Fatalf("CreateMarket failed: %v", err)
	}
	bet, err := service.PlaceBet(seller, market.ID, "A", 100)
	if err != nil {
		t.Fatalf("PlaceBet failed: %v", err)
	}
	listing, err := service.ListBetForSale(bet.ID, seller, 150)
	if err != nil {
		t.Fatalf("ListBetForSale failed: %v", err)
	}

	err = service.CancelListing(listing.ID, other)
	if !errors.Is(err, service.ErrNotListingOwner) {
		t.Errorf("expected ErrNotListingOwner, got %v", err)
	}
}

func TestCancelListing_RejectsAlreadySold(t *testing.T) {
	cleanTables(t)

	seller := uuid.New().String()
	buyer := uuid.New().String()
	syncUser(t, seller, 1000)
	syncUser(t, buyer, 1000)

	market, err := service.CreateMarket("Cancel Sold Test", []string{"A", "B"}, 0)
	if err != nil {
		t.Fatalf("CreateMarket failed: %v", err)
	}
	bet, err := service.PlaceBet(seller, market.ID, "A", 100)
	if err != nil {
		t.Fatalf("PlaceBet failed: %v", err)
	}
	listing, err := service.ListBetForSale(bet.ID, seller, 150)
	if err != nil {
		t.Fatalf("ListBetForSale failed: %v", err)
	}
	if _, err := service.BuyListedBet(listing.ID, buyer); err != nil {
		t.Fatalf("BuyListedBet failed: %v", err)
	}

	err = service.CancelListing(listing.ID, seller)
	if !errors.Is(err, service.ErrListingNotActive) {
		t.Errorf("expected ErrListingNotActive, got %v", err)
	}
}

func TestBuyListedBet_HappyPath(t *testing.T) {
	cleanTables(t)

	seller := uuid.New().String()
	buyer := uuid.New().String()
	syncUser(t, seller, 1000)
	syncUser(t, buyer, 1000)

	houseStart := getBalance(t, service.HouseUserID)

	market, err := service.CreateMarket("Buy Happy Path Test", []string{"A", "B"}, 0)
	if err != nil {
		t.Fatalf("CreateMarket failed: %v", err)
	}
	bet, err := service.PlaceBet(seller, market.ID, "A", 100)
	if err != nil {
		t.Fatalf("PlaceBet failed: %v", err)
	}
	listing, err := service.ListBetForSale(bet.ID, seller, 150)
	if err != nil {
		t.Fatalf("ListBetForSale failed: %v", err)
	}

	got, err := service.BuyListedBet(listing.ID, buyer)
	if err != nil {
		t.Fatalf("BuyListedBet failed: %v", err)
	}
	if got.Status != "SOLD" || got.BuyerID == nil || *got.BuyerID != buyer {
		t.Errorf("expected SOLD listing with buyer set, got %+v", got)
	}

	sellerBal := getBalance(t, seller)
	buyerBal := getBalance(t, buyer)
	houseBal := getBalance(t, service.HouseUserID)

	if sellerBal != 900+150 { // 1000 - 100 staked + 150 sale price
		t.Errorf("expected seller balance 1050, got %d", sellerBal)
	}
	if buyerBal != 1000-150 {
		t.Errorf("expected buyer balance 850, got %d", buyerBal)
	}
	if houseBal != houseStart {
		t.Errorf("expected house balance untouched by a sale, got diff %d", houseBal-houseStart)
	}

	bets, err := service.ListUserBets(buyer)
	if err != nil {
		t.Fatalf("ListUserBets failed: %v", err)
	}
	found := false
	for _, b := range bets {
		if b.ID == bet.ID {
			found = true
		}
	}
	if !found {
		t.Error("expected the bet to now belong to the buyer")
	}
}

func TestBuyListedBet_RejectsSelfPurchase(t *testing.T) {
	cleanTables(t)

	seller := uuid.New().String()
	syncUser(t, seller, 1000)

	market, err := service.CreateMarket("Self Purchase Test", []string{"A", "B"}, 0)
	if err != nil {
		t.Fatalf("CreateMarket failed: %v", err)
	}
	bet, err := service.PlaceBet(seller, market.ID, "A", 100)
	if err != nil {
		t.Fatalf("PlaceBet failed: %v", err)
	}
	listing, err := service.ListBetForSale(bet.ID, seller, 150)
	if err != nil {
		t.Fatalf("ListBetForSale failed: %v", err)
	}

	_, err = service.BuyListedBet(listing.ID, seller)
	if !errors.Is(err, service.ErrCannotBuyOwnListing) {
		t.Errorf("expected ErrCannotBuyOwnListing, got %v", err)
	}
}

func TestBuyListedBet_RejectsInsufficientBalance(t *testing.T) {
	cleanTables(t)

	seller := uuid.New().String()
	buyer := uuid.New().String()
	syncUser(t, seller, 1000)
	syncUser(t, buyer, 50) // less than the asking price

	market, err := service.CreateMarket("Insufficient Balance Test", []string{"A", "B"}, 0)
	if err != nil {
		t.Fatalf("CreateMarket failed: %v", err)
	}
	bet, err := service.PlaceBet(seller, market.ID, "A", 100)
	if err != nil {
		t.Fatalf("PlaceBet failed: %v", err)
	}
	listing, err := service.ListBetForSale(bet.ID, seller, 150)
	if err != nil {
		t.Fatalf("ListBetForSale failed: %v", err)
	}

	_, err = service.BuyListedBet(listing.ID, buyer)
	if !errors.Is(err, service.ErrInsufficientBalance) {
		t.Errorf("expected ErrInsufficientBalance, got %v", err)
	}

	// Full rollback: no balance/ownership/listing-status changes.
	if getBalance(t, buyer) != 50 {
		t.Errorf("expected buyer balance unchanged at 50, got %d", getBalance(t, buyer))
	}
	if getBalance(t, seller) != 900 {
		t.Errorf("expected seller balance unchanged at 900, got %d", getBalance(t, seller))
	}
	active, err := service.ListActiveListingsForMarket(market.ID)
	if err != nil {
		t.Fatalf("ListActiveListingsForMarket failed: %v", err)
	}
	if len(active) != 1 {
		t.Errorf("expected the listing to remain active, got %d active listings", len(active))
	}
}

func TestBuyListedBet_ConcurrentDoubleBuy(t *testing.T) {
	cleanTables(t)

	seller := uuid.New().String()
	buyer1 := uuid.New().String()
	buyer2 := uuid.New().String()
	syncUser(t, seller, 1000)
	syncUser(t, buyer1, 1000)
	syncUser(t, buyer2, 1000)

	market, err := service.CreateMarket("Concurrent Double Buy Test", []string{"A", "B"}, 0)
	if err != nil {
		t.Fatalf("CreateMarket failed: %v", err)
	}
	bet, err := service.PlaceBet(seller, market.ID, "A", 100)
	if err != nil {
		t.Fatalf("PlaceBet failed: %v", err)
	}
	listing, err := service.ListBetForSale(bet.ID, seller, 150)
	if err != nil {
		t.Fatalf("ListBetForSale failed: %v", err)
	}

	var wg sync.WaitGroup
	var mu sync.Mutex
	successCount := 0
	failCount := 0

	for _, buyer := range []string{buyer1, buyer2} {
		wg.Add(1)
		go func(b string) {
			defer wg.Done()
			_, err := service.BuyListedBet(listing.ID, b)
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				successCount++
			} else {
				failCount++
			}
		}(buyer)
	}
	wg.Wait()

	if successCount != 1 {
		t.Errorf("expected exactly 1 successful buy, got %d", successCount)
	}
	if failCount != 1 {
		t.Errorf("expected exactly 1 failed buy, got %d", failCount)
	}

	sellerBal := getBalance(t, seller)
	if sellerBal != 900+150 {
		t.Errorf("expected seller credited exactly once (1050), got %d", sellerBal)
	}
}

func TestBuyListedBet_RejectsStaleListingAfterLock(t *testing.T) {
	cleanTables(t)

	seller := uuid.New().String()
	buyer := uuid.New().String()
	syncUser(t, seller, 1000)
	syncUser(t, buyer, 1000)

	market, err := service.CreateMarket("Stale After Lock Test", []string{"A", "B"}, 0)
	if err != nil {
		t.Fatalf("CreateMarket failed: %v", err)
	}
	bet, err := service.PlaceBet(seller, market.ID, "A", 100)
	if err != nil {
		t.Fatalf("PlaceBet failed: %v", err)
	}
	listing, err := service.ListBetForSale(bet.ID, seller, 150)
	if err != nil {
		t.Fatalf("ListBetForSale failed: %v", err)
	}

	if err := service.LockMarket(market.ID); err != nil {
		t.Fatalf("LockMarket failed: %v", err)
	}

	_, err = service.BuyListedBet(listing.ID, buyer)
	if !errors.Is(err, service.ErrListingMarketNoLongerOpen) {
		t.Errorf("expected ErrListingMarketNoLongerOpen, got %v", err)
	}

	active, err := service.ListActiveListingsForMarket(market.ID)
	if err != nil {
		t.Fatalf("ListActiveListingsForMarket failed: %v", err)
	}
	if len(active) != 0 {
		t.Errorf("expected the stale listing to be auto-cancelled, got %d active", len(active))
	}
}

func TestBuyListedBet_RejectsStaleListingAfterCancelMarket(t *testing.T) {
	cleanTables(t)

	seller := uuid.New().String()
	buyer := uuid.New().String()
	syncUser(t, seller, 1000)
	syncUser(t, buyer, 1000)

	market, err := service.CreateMarket("Stale After Cancel Test", []string{"A", "B"}, 0)
	if err != nil {
		t.Fatalf("CreateMarket failed: %v", err)
	}
	bet, err := service.PlaceBet(seller, market.ID, "A", 100)
	if err != nil {
		t.Fatalf("PlaceBet failed: %v", err)
	}
	listing, err := service.ListBetForSale(bet.ID, seller, 150)
	if err != nil {
		t.Fatalf("ListBetForSale failed: %v", err)
	}

	if err := service.CancelMarket(market.ID); err != nil {
		t.Fatalf("CancelMarket failed: %v", err)
	}

	_, err = service.BuyListedBet(listing.ID, buyer)
	if !errors.Is(err, service.ErrListingMarketNoLongerOpen) {
		t.Errorf("expected ErrListingMarketNoLongerOpen, got %v", err)
	}
}

func TestResolveMarket_PaysNewOwnerAfterResale(t *testing.T) {
	cleanTables(t)

	seller := uuid.New().String()
	buyer := uuid.New().String()
	syncUser(t, seller, 1000)
	syncUser(t, buyer, 1000)

	market, err := service.CreateMarket("Payout After Resale Test", []string{"A", "B"}, 0)
	if err != nil {
		t.Fatalf("CreateMarket failed: %v", err)
	}
	// Someone needs to bet on B too, or poolVencedor for A will be exactly
	// the seller's stake and things still work, but keep it simple: only A
	// has action, seed is 0, so B's pool would be 0 - fine, A still resolves
	// cleanly since poolVencedor (A) > 0 regardless of B.
	bet, err := service.PlaceBet(seller, market.ID, "A", 100)
	if err != nil {
		t.Fatalf("PlaceBet failed: %v", err)
	}
	listing, err := service.ListBetForSale(bet.ID, seller, 150)
	if err != nil {
		t.Fatalf("ListBetForSale failed: %v", err)
	}
	if _, err := service.BuyListedBet(listing.ID, buyer); err != nil {
		t.Fatalf("BuyListedBet failed: %v", err)
	}

	sellerAfterSale := getBalance(t, seller)
	buyerAfterSale := getBalance(t, buyer)

	if err := service.LockMarket(market.ID); err != nil {
		t.Fatalf("LockMarket failed: %v", err)
	}
	if err := service.ResolveMarket(market.ID, "A"); err != nil {
		t.Fatalf("ResolveMarket failed: %v", err)
	}

	// Only one winning bet (the resold one, now owned by buyer) - buyer
	// takes the whole distributable pool, seller gets nothing further.
	sellerFinal := getBalance(t, seller)
	buyerFinal := getBalance(t, buyer)

	if sellerFinal != sellerAfterSale {
		t.Errorf("expected seller balance unchanged by resolution (no longer holds the winning bet), got diff %d", sellerFinal-sellerAfterSale)
	}
	if buyerFinal <= buyerAfterSale {
		t.Errorf("expected buyer to receive the payout for the bet they bought, balance did not increase (before %d, after %d)", buyerAfterSale, buyerFinal)
	}
}

func TestConservation_SaleNeverTouchesHouseBalance(t *testing.T) {
	cleanTables(t)

	seller := uuid.New().String()
	buyer := uuid.New().String()
	syncUser(t, seller, 1000)
	syncUser(t, buyer, 1000)

	houseBefore := getBalance(t, service.HouseUserID)

	market, err := service.CreateMarket("House Untouched Test", []string{"A", "B"}, 20)
	if err != nil {
		t.Fatalf("CreateMarket failed: %v", err)
	}
	bet, err := service.PlaceBet(seller, market.ID, "A", 100)
	if err != nil {
		t.Fatalf("PlaceBet failed: %v", err)
	}
	houseAfterSeed := getBalance(t, service.HouseUserID)

	listing, err := service.ListBetForSale(bet.ID, seller, 150)
	if err != nil {
		t.Fatalf("ListBetForSale failed: %v", err)
	}
	if _, err := service.BuyListedBet(listing.ID, buyer); err != nil {
		t.Fatalf("BuyListedBet failed: %v", err)
	}

	houseAfterSale := getBalance(t, service.HouseUserID)
	if houseAfterSale != houseAfterSeed {
		t.Errorf("expected house balance unaffected by the sale itself (before %d, after %d)", houseAfterSeed, houseAfterSale)
	}
	_ = houseBefore
}

func TestListMarketListings_ExcludesNonListedStatuses(t *testing.T) {
	cleanTables(t)

	seller := uuid.New().String()
	buyer := uuid.New().String()
	syncUser(t, seller, 1000)
	syncUser(t, buyer, 1000)

	market, err := service.CreateMarket("Excludes Non-listed Test", []string{"A", "B"}, 0)
	if err != nil {
		t.Fatalf("CreateMarket failed: %v", err)
	}

	bet1, err := service.PlaceBet(seller, market.ID, "A", 100)
	if err != nil {
		t.Fatalf("PlaceBet failed: %v", err)
	}
	bet2, err := service.PlaceBet(seller, market.ID, "B", 100)
	if err != nil {
		t.Fatalf("PlaceBet failed: %v", err)
	}

	listing1, err := service.ListBetForSale(bet1.ID, seller, 50)
	if err != nil {
		t.Fatalf("ListBetForSale failed: %v", err)
	}
	listing2, err := service.ListBetForSale(bet2.ID, seller, 50)
	if err != nil {
		t.Fatalf("ListBetForSale failed: %v", err)
	}

	if err := service.CancelListing(listing1.ID, seller); err != nil {
		t.Fatalf("CancelListing failed: %v", err)
	}
	if _, err := service.BuyListedBet(listing2.ID, buyer); err != nil {
		t.Fatalf("BuyListedBet failed: %v", err)
	}

	active, err := service.ListActiveListingsForMarket(market.ID)
	if err != nil {
		t.Fatalf("ListActiveListingsForMarket failed: %v", err)
	}
	if len(active) != 0 {
		t.Errorf("expected no active listings after one cancel + one sale, got %d", len(active))
	}
}
