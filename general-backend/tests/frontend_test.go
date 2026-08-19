package tests

import (
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
)

func TestHome_Smoke(t *testing.T) {
	r := newFullRouter()
	w := doFormRequest(t, r, http.MethodGet, "/", nil, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected home page 200, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "JOGOS EM ABERTO") {
		t.Fatalf("expected home page markup, got %s", w.Body.String())
	}
}

func TestMarketDetail_UnknownID(t *testing.T) {
	r := newFullRouter()
	w := doFormRequest(t, r, http.MethodGet, "/markets/00000000-0000-0000-0000-000000000099", nil, nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown market, got %d", w.Code)
	}
}

func TestDeposit_UpdatesBalance(t *testing.T) {
	cleanAccounts(t)
	r := newFullRouter()
	cookies := registerAndLogin(t, r, uniqueEmail(), "hunter2")

	depositW := doFormRequest(t, r, http.MethodPost, "/profile/deposit", url.Values{
		"amount": {"250"},
	}, cookies)
	if depositW.Code != http.StatusFound {
		t.Fatalf("expected deposit to redirect, got %d: %s", depositW.Code, depositW.Body.String())
	}

	profileW := doFormRequest(t, r, http.MethodGet, "/profile", nil, cookies)
	if !strings.Contains(profileW.Body.String(), "R$ 250,00") {
		t.Fatalf("expected profile to show updated balance, got %s", profileW.Body.String())
	}
}

func TestPlaceBetPage_UpdatesPoolsAndActivityFeed(t *testing.T) {
	cleanAccounts(t)
	r := newFullRouter()
	adminEmail := uniqueEmail()
	os.Setenv("INITIAL_ADMIN_EMAIL", adminEmail)
	t.Cleanup(func() { os.Unsetenv("INITIAL_ADMIN_EMAIL") })
	adminCookies := registerAndLogin(t, r, adminEmail, "hunter2")
	bettorCookies := registerAndLogin(t, r, uniqueEmail(), "hunter2")

	createW := doFormRequest(t, r, http.MethodPost, "/admin/markets", url.Values{
		"question": {"Vai bater o recorde?"},
		"seed":     {"50"},
	}, adminCookies)
	marketID := strings.TrimPrefix(createW.Header().Get("Location"), "/markets/")

	doFormRequest(t, r, http.MethodPost, "/profile/deposit", url.Values{"amount": {"1000"}}, bettorCookies)

	betW := doFormRequest(t, r, http.MethodPost, "/markets/"+marketID+"/place-bet", url.Values{
		"outcome": {"SIM"},
		"stake":   {"200"},
	}, bettorCookies)
	if betW.Code != http.StatusOK {
		t.Fatalf("expected bet placement to succeed, got %d: %s", betW.Code, betW.Body.String())
	}
	if strings.Contains(betW.Body.String(), "text-tche-red") {
		t.Fatalf("expected a success (green) response, got %s", betW.Body.String())
	}

	// Pools updated: seed 50 + bet 200 = 250 in SIM.
	apiW := doRequest(t, r, http.MethodGet, "/markets/"+marketID, nil)
	body := decodeJSON(t, apiW)
	pools := body["pools"].(map[string]interface{})
	if pools["SIM"].(float64) != 250 {
		t.Fatalf("expected SIM pool to be 250, got %v", pools["SIM"])
	}

	// Shows up in the market page's activity feed (Accept: text/html branch).
	pageW := doFormRequest(t, r, http.MethodGet, "/markets/"+marketID, nil, nil)
	if !strings.Contains(pageW.Body.String(), "apostou SIM") {
		t.Fatalf("expected activity feed to show the new bet, got %s", pageW.Body.String())
	}

	// And in the bettor's own profile bet history.
	profileW := doFormRequest(t, r, http.MethodGet, "/profile", nil, bettorCookies)
	if !strings.Contains(profileW.Body.String(), "Vai bater o recorde?") {
		t.Fatalf("expected bet history to show the market question, got %s", profileW.Body.String())
	}
}

func TestPlaceBetPage_RequiresLogin(t *testing.T) {
	cleanAccounts(t)
	r := newFullRouter()
	adminEmail := uniqueEmail()
	os.Setenv("INITIAL_ADMIN_EMAIL", adminEmail)
	t.Cleanup(func() { os.Unsetenv("INITIAL_ADMIN_EMAIL") })
	adminCookies := registerAndLogin(t, r, adminEmail, "hunter2")

	createW := doFormRequest(t, r, http.MethodPost, "/admin/markets", url.Values{
		"question": {"Vai ganhar o campeonato?"},
		"seed":     {"10"},
	}, adminCookies)
	marketID := strings.TrimPrefix(createW.Header().Get("Location"), "/markets/")

	w := doFormRequest(t, r, http.MethodPost, "/markets/"+marketID+"/place-bet", url.Values{
		"outcome": {"SIM"},
		"stake":   {"50"},
	}, nil)
	if w.Code != http.StatusFound {
		t.Fatalf("expected anonymous bet placement to redirect to login, got %d: %s", w.Code, w.Body.String())
	}
}
