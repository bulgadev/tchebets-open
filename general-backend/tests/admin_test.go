package tests

import (
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
)

func TestAdminBootstrap_MatchingEmailBecomesAdmin(t *testing.T) {
	cleanAccounts(t)
	r := newFullRouter()
	email := uniqueEmail()
	os.Setenv("INITIAL_ADMIN_EMAIL", email)
	t.Cleanup(func() { os.Unsetenv("INITIAL_ADMIN_EMAIL") })

	cookies := registerAndLogin(t, r, email, "hunter2")

	w := doFormRequest(t, r, http.MethodGet, "/admin/markets", nil, cookies)
	if w.Code != http.StatusOK {
		t.Fatalf("expected account matching INITIAL_ADMIN_EMAIL to be admin, got %d: %s", w.Code, w.Body.String())
	}
}

func TestAdminBootstrap_SecondAccountIsNotAdmin(t *testing.T) {
	cleanAccounts(t)
	r := newFullRouter()
	email := uniqueEmail()
	os.Setenv("INITIAL_ADMIN_EMAIL", email)
	t.Cleanup(func() { os.Unsetenv("INITIAL_ADMIN_EMAIL") })

	registerAndLogin(t, r, email, "hunter2")
	// Directly disproves the old "first signup wins" behavior: a second
	// account, registered after the configured admin, must NOT be admin.
	secondCookies := registerAndLogin(t, r, uniqueEmail(), "hunter2")

	w := doFormRequest(t, r, http.MethodGet, "/admin/markets", nil, secondCookies)
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected second account to be rejected as non-admin, got %d: %s", w.Code, w.Body.String())
	}
}

func TestAdminBootstrap_NoEnvVarMeansNoAutoAdmin(t *testing.T) {
	cleanAccounts(t)
	r := newFullRouter()
	os.Unsetenv("INITIAL_ADMIN_EMAIL")

	cookies := registerAndLogin(t, r, uniqueEmail(), "hunter2")

	w := doFormRequest(t, r, http.MethodGet, "/admin/markets", nil, cookies)
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected no account to be auto-admin when INITIAL_ADMIN_EMAIL is unset, got %d: %s", w.Code, w.Body.String())
	}
}

func TestAdminPromoteUser_RequiresAdmin(t *testing.T) {
	cleanAccounts(t)
	r := newFullRouter()
	nonAdminCookies := registerAndLogin(t, r, uniqueEmail(), "hunter2")

	w := doFormRequest(t, r, http.MethodPost, "/admin/users/promote", url.Values{
		"email": {uniqueEmail()},
	}, nonAdminCookies)
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for non-admin promoting a user, got %d: %s", w.Code, w.Body.String())
	}
}

func TestAdminPromoteUser_HappyPath(t *testing.T) {
	cleanAccounts(t)
	r := newFullRouter()
	adminEmail := uniqueEmail()
	os.Setenv("INITIAL_ADMIN_EMAIL", adminEmail)
	t.Cleanup(func() { os.Unsetenv("INITIAL_ADMIN_EMAIL") })
	adminCookies := registerAndLogin(t, r, adminEmail, "hunter2")

	targetEmail := uniqueEmail()
	targetCookies := registerAndLogin(t, r, targetEmail, "hunter2")

	// Not admin yet.
	preW := doFormRequest(t, r, http.MethodGet, "/admin/markets", nil, targetCookies)
	if preW.Code != http.StatusForbidden {
		t.Fatalf("expected target account to start non-admin, got %d", preW.Code)
	}

	promoteW := doFormRequest(t, r, http.MethodPost, "/admin/users/promote", url.Values{
		"email": {targetEmail},
	}, adminCookies)
	if promoteW.Code != http.StatusFound {
		t.Fatalf("expected promote to redirect, got %d: %s", promoteW.Code, promoteW.Body.String())
	}

	postW := doFormRequest(t, r, http.MethodGet, "/admin/markets", nil, targetCookies)
	if postW.Code != http.StatusOK {
		t.Fatalf("expected promoted account to reach /admin/markets, got %d: %s", postW.Code, postW.Body.String())
	}
}

func TestAdmin_CreateLockResolveFlow(t *testing.T) {
	cleanAccounts(t)
	r := newFullRouter()
	adminEmail := uniqueEmail()
	os.Setenv("INITIAL_ADMIN_EMAIL", adminEmail)
	t.Cleanup(func() { os.Unsetenv("INITIAL_ADMIN_EMAIL") })
	adminCookies := registerAndLogin(t, r, adminEmail, "hunter2")

	createW := doFormRequest(t, r, http.MethodPost, "/admin/markets", url.Values{
		"question": {"Vai chover amanhã?"},
		"seed":     {"20"},
	}, adminCookies)
	if createW.Code != http.StatusFound {
		t.Fatalf("expected market creation to redirect, got %d: %s", createW.Code, createW.Body.String())
	}
	loc := createW.Header().Get("Location")
	marketID := strings.TrimPrefix(loc, "/markets/")
	if marketID == "" || marketID == loc {
		t.Fatalf("expected redirect to /markets/<id>, got %q", loc)
	}

	// Shows up in the admin listing.
	listW := doFormRequest(t, r, http.MethodGet, "/admin/markets", nil, adminCookies)
	if listW.Code != http.StatusOK || !strings.Contains(listW.Body.String(), "Vai chover amanhã?") {
		t.Fatalf("expected new market to appear in admin listing, got %d: %s", listW.Code, listW.Body.String())
	}

	// Also shows up in the public JSON list endpoint.
	apiListW := doRequest(t, r, http.MethodGet, "/markets", nil)
	if apiListW.Code != http.StatusOK || !strings.Contains(apiListW.Body.String(), marketID) {
		t.Fatalf("expected new market in GET /markets, got %d: %s", apiListW.Code, apiListW.Body.String())
	}

	lockW := doFormRequest(t, r, http.MethodPost, "/admin/markets/"+marketID+"/lock", nil, adminCookies)
	if lockW.Code != http.StatusFound {
		t.Fatalf("expected lock to redirect, got %d: %s", lockW.Code, lockW.Body.String())
	}

	resolveW := doFormRequest(t, r, http.MethodPost, "/admin/markets/"+marketID+"/resolve", url.Values{
		"winning_outcome": {"SIM"},
	}, adminCookies)
	if resolveW.Code != http.StatusFound {
		t.Fatalf("expected resolve to redirect, got %d: %s", resolveW.Code, resolveW.Body.String())
	}

	getW := doRequest(t, r, http.MethodGet, "/markets/"+marketID, nil)
	body := decodeJSON(t, getW)
	if body["status"] != "RESOLVED" {
		t.Fatalf("expected market RESOLVED after admin flow, got %v", body["status"])
	}
}

func TestAdmin_CreateMarket_TeamPickerHappyPath(t *testing.T) {
	cleanAccounts(t)
	r := newFullRouter()
	adminEmail := uniqueEmail()
	os.Setenv("INITIAL_ADMIN_EMAIL", adminEmail)
	t.Cleanup(func() { os.Unsetenv("INITIAL_ADMIN_EMAIL") })
	adminCookies := registerAndLogin(t, r, adminEmail, "hunter2")

	createW := doFormRequest(t, r, http.MethodPost, "/admin/markets", url.Values{
		"team_a": {"Trakinas Fc"},
		"team_b": {"Legados Fc"},
		"seed":   {"10"},
	}, adminCookies)
	if createW.Code != http.StatusFound {
		t.Fatalf("expected market creation to redirect, got %d: %s", createW.Code, createW.Body.String())
	}
	marketID := strings.TrimPrefix(createW.Header().Get("Location"), "/markets/")

	getW := doRequest(t, r, http.MethodGet, "/markets/"+marketID, nil)
	body := decodeJSON(t, getW)
	if body["question"] != "Trakinas Fc vencerá o Legados Fc?" {
		t.Fatalf("expected auto-generated question, got %v", body["question"])
	}
}

func TestAdmin_CreateMarket_RejectsUnknownTeam(t *testing.T) {
	cleanAccounts(t)
	r := newFullRouter()
	adminEmail := uniqueEmail()
	os.Setenv("INITIAL_ADMIN_EMAIL", adminEmail)
	t.Cleanup(func() { os.Unsetenv("INITIAL_ADMIN_EMAIL") })
	adminCookies := registerAndLogin(t, r, adminEmail, "hunter2")

	createW := doFormRequest(t, r, http.MethodPost, "/admin/markets", url.Values{
		"team_a": {"Time Inventado FC"},
		"team_b": {"Legados Fc"},
		"seed":   {"10"},
	}, adminCookies)
	if createW.Code != http.StatusOK || !strings.Contains(createW.Body.String(), "time desconhecido") {
		t.Fatalf("expected rejection of an unknown team, got %d: %s", createW.Code, createW.Body.String())
	}
}

func TestAdmin_CreateMarket_RejectsSameTeamTwice(t *testing.T) {
	cleanAccounts(t)
	r := newFullRouter()
	adminEmail := uniqueEmail()
	os.Setenv("INITIAL_ADMIN_EMAIL", adminEmail)
	t.Cleanup(func() { os.Unsetenv("INITIAL_ADMIN_EMAIL") })
	adminCookies := registerAndLogin(t, r, adminEmail, "hunter2")

	createW := doFormRequest(t, r, http.MethodPost, "/admin/markets", url.Values{
		"team_a": {"Trakinas Fc"},
		"team_b": {"Trakinas Fc"},
		"seed":   {"10"},
	}, adminCookies)
	if createW.Code != http.StatusOK || !strings.Contains(createW.Body.String(), "diferentes") {
		t.Fatalf("expected rejection of picking the same team twice, got %d: %s", createW.Code, createW.Body.String())
	}
}

func TestAdmin_CancelMarket(t *testing.T) {
	cleanAccounts(t)
	r := newFullRouter()
	adminEmail := uniqueEmail()
	os.Setenv("INITIAL_ADMIN_EMAIL", adminEmail)
	t.Cleanup(func() { os.Unsetenv("INITIAL_ADMIN_EMAIL") })
	adminCookies := registerAndLogin(t, r, adminEmail, "hunter2")

	createW := doFormRequest(t, r, http.MethodPost, "/admin/markets", url.Values{
		"question": {"Vai nevar em julho?"},
		"seed":     {"0"},
	}, adminCookies)
	loc := createW.Header().Get("Location")
	marketID := strings.TrimPrefix(loc, "/markets/")

	cancelW := doFormRequest(t, r, http.MethodPost, "/admin/markets/"+marketID+"/cancel", nil, adminCookies)
	if cancelW.Code != http.StatusFound {
		t.Fatalf("expected cancel to redirect, got %d: %s", cancelW.Code, cancelW.Body.String())
	}

	getW := doRequest(t, r, http.MethodGet, "/markets/"+marketID, nil)
	body := decodeJSON(t, getW)
	if body["status"] != "CANCELLED" {
		t.Fatalf("expected market CANCELLED, got %v", body["status"])
	}
}
