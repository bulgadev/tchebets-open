package tests

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func decodeJSONArray(t *testing.T, w *httptest.ResponseRecorder) []map[string]interface{} {
	t.Helper()
	var out []map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("failed to decode response body array %q: %v", w.Body.String(), err)
	}
	return out
}

// createOpenMarketAndBet registers an admin (via INITIAL_ADMIN_EMAIL),
// creates an OPEN market, funds and logs in a bettor, and places one 200-
// stake bet - returning the market id and the bettor's cookies so tests can
// then exercise the sell/buy flow on top of it.
func createOpenMarketAndBet(t *testing.T, r *gin.Engine) (marketID string, bettorCookies []*http.Cookie) {
	t.Helper()

	adminEmail := uniqueEmail()
	os.Setenv("INITIAL_ADMIN_EMAIL", adminEmail)
	t.Cleanup(func() { os.Unsetenv("INITIAL_ADMIN_EMAIL") })
	adminCookies := registerAndLogin(t, r, adminEmail, "hunter2")

	createW := doFormRequest(t, r, http.MethodPost, "/admin/markets", url.Values{
		"question": {"Vai vender a aposta?"},
		"seed":     {"0"},
	}, adminCookies)
	marketID = strings.TrimPrefix(createW.Header().Get("Location"), "/markets/")

	bettorCookies = registerAndLogin(t, r, uniqueEmail(), "hunter2")
	doFormRequest(t, r, http.MethodPost, "/profile/deposit", url.Values{"amount": {"1000"}}, bettorCookies)
	doFormRequest(t, r, http.MethodPost, "/markets/"+marketID+"/place-bet", url.Values{
		"outcome": {"SIM"},
		"stake":   {"200"},
	}, bettorCookies)

	return marketID, bettorCookies
}

// findBetID pulls the id of the 200-stake bet out of GET /markets/:id/bets.
func findBetID(t *testing.T, r *gin.Engine, marketID string) string {
	t.Helper()
	betsW := doRequest(t, r, http.MethodGet, "/markets/"+marketID+"/bets", nil)
	for _, b := range decodeJSONArray(t, betsW) {
		if stake, _ := b["stake"].(float64); stake == 200 {
			return b["id"].(string)
		}
	}
	t.Fatal("could not find the placed bet's id")
	return ""
}

// findActiveListingID pulls the id of the (single) active listing out of
// GET /markets/:id/listings.
func findActiveListingID(t *testing.T, r *gin.Engine, marketID string) string {
	t.Helper()
	listingsW := doRequest(t, r, http.MethodGet, "/markets/"+marketID+"/listings", nil)
	rows := decodeJSONArray(t, listingsW)
	if len(rows) != 1 {
		t.Fatalf("expected exactly 1 active listing, got %d", len(rows))
	}
	return rows[0]["id"].(string)
}

func TestListBetForSalePage_RequiresLogin(t *testing.T) {
	cleanAccounts(t)
	r := newFullRouter()

	w := doFormRequest(t, r, http.MethodPost, "/list-bet/00000000-0000-0000-0000-000000000001", url.Values{
		"asking_price": {"100"},
	}, nil)
	if w.Code != http.StatusFound {
		t.Fatalf("expected anonymous list-for-sale to redirect to login, got %d", w.Code)
	}
	loc := w.Header().Get("Location")
	if !strings.HasPrefix(loc, "/login") {
		t.Fatalf("expected redirect to /login, got %q", loc)
	}
}

func TestBuyListingPage_RequiresLogin(t *testing.T) {
	cleanAccounts(t)
	r := newFullRouter()

	// /buy-listing/:id sits behind RequireUser(), same as every other authed
	// route - the middleware redirects to /login before BuyListingPage's own
	// handler ever runs.
	w := doFormRequest(t, r, http.MethodPost, "/buy-listing/00000000-0000-0000-0000-000000000001", nil, nil)
	if w.Code != http.StatusFound {
		t.Fatalf("expected anonymous buy attempt to redirect to login, got %d: %s", w.Code, w.Body.String())
	}
	loc := w.Header().Get("Location")
	if !strings.HasPrefix(loc, "/login") {
		t.Fatalf("expected redirect to /login, got %q", loc)
	}
}

func TestCancelListingPage_RequiresLogin(t *testing.T) {
	cleanAccounts(t)
	r := newFullRouter()

	w := doFormRequest(t, r, http.MethodPost, "/cancel-listing/00000000-0000-0000-0000-000000000001", nil, nil)
	if w.Code != http.StatusFound {
		t.Fatalf("expected anonymous cancel attempt to redirect to login, got %d", w.Code)
	}
}

func TestListingFlow_SellThenBuy(t *testing.T) {
	cleanAccounts(t)
	r := newFullRouter()
	marketID, sellerCookies := createOpenMarketAndBet(t, r)

	// Bet shows up in the profile with a "Vender" action, since the market is OPEN.
	profileW := doFormRequest(t, r, http.MethodGet, "/profile", nil, sellerCookies)
	if !strings.Contains(profileW.Body.String(), "Vender") {
		t.Fatalf("expected profile to show a sell action for an OPEN-market bet, got %s", profileW.Body.String())
	}

	betID := findBetID(t, r, marketID)

	listW := doFormRequest(t, r, http.MethodPost, "/list-bet/"+betID, url.Values{
		"asking_price": {"250"},
	}, sellerCookies)
	if listW.Code != http.StatusFound {
		t.Fatalf("expected listing to redirect, got %d: %s", listW.Code, listW.Body.String())
	}

	// Now shows up on the market page's "for sale" section.
	marketPageW := doFormRequest(t, r, http.MethodGet, "/markets/"+marketID, nil, nil)
	if !strings.Contains(marketPageW.Body.String(), "À Venda") {
		t.Fatalf("expected market page to show the for-sale section, got %s", marketPageW.Body.String())
	}

	// Profile now shows "Aguardando comprador" instead of the sell form.
	profileAfterListW := doFormRequest(t, r, http.MethodGet, "/profile", nil, sellerCookies)
	if !strings.Contains(profileAfterListW.Body.String(), "Aguardando comprador") {
		t.Fatalf("expected profile to show the pending-sale state, got %s", profileAfterListW.Body.String())
	}

	// A second user buys it.
	buyerCookies := registerAndLogin(t, r, uniqueEmail(), "hunter2")
	doFormRequest(t, r, http.MethodPost, "/profile/deposit", url.Values{"amount": {"1000"}}, buyerCookies)

	listingID := findActiveListingID(t, r, marketID)

	buyW := doFormRequest(t, r, http.MethodPost, "/buy-listing/"+listingID, nil, buyerCookies)
	if buyW.Code != http.StatusOK || strings.Contains(buyW.Body.String(), "text-tche-red") {
		t.Fatalf("expected buy to succeed, got %d: %s", buyW.Code, buyW.Body.String())
	}

	// Bet history for the buyer now includes this market's bet.
	buyerProfileW := doFormRequest(t, r, http.MethodGet, "/profile", nil, buyerCookies)
	if !strings.Contains(buyerProfileW.Body.String(), "Vai vender a aposta?") {
		t.Fatalf("expected buyer's profile to show the acquired bet, got %s", buyerProfileW.Body.String())
	}

	// The for-sale section is now empty again (sold, no longer LISTED).
	afterBuyMarketPageW := doFormRequest(t, r, http.MethodGet, "/markets/"+marketID, nil, nil)
	if strings.Contains(afterBuyMarketPageW.Body.String(), "À Venda") {
		t.Fatalf("expected the for-sale section to disappear once sold, got %s", afterBuyMarketPageW.Body.String())
	}
}

func TestListBetForSalePage_RejectsListingSomeoneElsesBet(t *testing.T) {
	cleanAccounts(t)
	r := newFullRouter()
	marketID, _ := createOpenMarketAndBet(t, r)
	betID := findBetID(t, r, marketID)

	otherCookies := registerAndLogin(t, r, uniqueEmail(), "hunter2")
	w := doFormRequest(t, r, http.MethodPost, "/list-bet/"+betID, url.Values{
		"asking_price": {"999"},
	}, otherCookies)
	if w.Code != http.StatusFound {
		t.Fatalf("expected the (failed) listing attempt to still redirect with a flash, got %d", w.Code)
	}

	listingsW := doRequest(t, r, http.MethodGet, "/markets/"+marketID+"/listings", nil)
	if rows := decodeJSONArray(t, listingsW); len(rows) != 0 {
		t.Fatalf("expected no listing to have been created by a non-owner, got %d", len(rows))
	}
}

func TestBuyListingPage_RejectsSelfBuy(t *testing.T) {
	cleanAccounts(t)
	r := newFullRouter()
	marketID, sellerCookies := createOpenMarketAndBet(t, r)
	betID := findBetID(t, r, marketID)

	doFormRequest(t, r, http.MethodPost, "/list-bet/"+betID, url.Values{"asking_price": {"250"}}, sellerCookies)
	listingID := findActiveListingID(t, r, marketID)

	w := doFormRequest(t, r, http.MethodPost, "/buy-listing/"+listingID, nil, sellerCookies)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "text-tche-red") {
		t.Fatalf("expected self-buy to fail with a red result, got %d: %s", w.Code, w.Body.String())
	}
}

func TestProfilePage_HidesSellActionForNonOpenMarketBet(t *testing.T) {
	cleanAccounts(t)
	r := newFullRouter()

	adminEmail := uniqueEmail()
	os.Setenv("INITIAL_ADMIN_EMAIL", adminEmail)
	t.Cleanup(func() { os.Unsetenv("INITIAL_ADMIN_EMAIL") })
	adminCookies := registerAndLogin(t, r, adminEmail, "hunter2")

	createW := doFormRequest(t, r, http.MethodPost, "/admin/markets", url.Values{
		"question": {"Mercado que vai fechar"},
		"seed":     {"0"},
	}, adminCookies)
	marketID := strings.TrimPrefix(createW.Header().Get("Location"), "/markets/")

	bettorCookies := registerAndLogin(t, r, uniqueEmail(), "hunter2")
	doFormRequest(t, r, http.MethodPost, "/profile/deposit", url.Values{"amount": {"1000"}}, bettorCookies)
	doFormRequest(t, r, http.MethodPost, "/markets/"+marketID+"/place-bet", url.Values{
		"outcome": {"SIM"},
		"stake":   {"200"},
	}, bettorCookies)

	// Market OPEN: sell action present.
	openProfileW := doFormRequest(t, r, http.MethodGet, "/profile", nil, bettorCookies)
	if !strings.Contains(openProfileW.Body.String(), "Vender") {
		t.Fatalf("expected sell action to be present while market is OPEN")
	}

	doFormRequest(t, r, http.MethodPost, "/admin/markets/"+marketID+"/lock", nil, adminCookies)
	doFormRequest(t, r, http.MethodPost, "/admin/markets/"+marketID+"/resolve", url.Values{
		"winning_outcome": {"SIM"},
	}, adminCookies)

	// Market RESOLVED: sell action gone.
	closedProfileW := doFormRequest(t, r, http.MethodGet, "/profile", nil, bettorCookies)
	if strings.Contains(closedProfileW.Body.String(), "Vender") {
		t.Fatalf("expected sell action to be hidden once the market resolved, got %s", closedProfileW.Body.String())
	}
}
