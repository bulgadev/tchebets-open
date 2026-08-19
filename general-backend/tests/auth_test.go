package tests

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/google/uuid"
)

func uniqueEmail() string {
	return uuid.New().String() + "@example.com"
}

func TestRegister_HappyPath(t *testing.T) {
	cleanAccounts(t)
	r := newFullRouter()

	cookies := registerAndLogin(t, r, uniqueEmail(), "hunter2")
	if len(cookies) == 0 {
		t.Fatal("expected a session cookie after register")
	}

	w := doFormRequest(t, r, http.MethodGet, "/profile", nil, cookies)
	if w.Code != http.StatusOK {
		t.Fatalf("expected registered+logged-in user to reach /profile, got %d: %s", w.Code, w.Body.String())
	}
}

func TestRegister_RejectsDuplicateEmail(t *testing.T) {
	cleanAccounts(t)
	r := newFullRouter()
	email := uniqueEmail()

	registerAndLogin(t, r, email, "hunter2")

	w := doFormRequest(t, r, http.MethodPost, "/register", url.Values{
		"email":    {email},
		"password": {"different"},
	}, nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for duplicate email, got %d: %s", w.Code, w.Body.String())
	}
}

func TestLogin_WrongPassword(t *testing.T) {
	cleanAccounts(t)
	r := newFullRouter()
	email := uniqueEmail()
	registerAndLogin(t, r, email, "correct-password")

	w := doFormRequest(t, r, http.MethodPost, "/login", url.Values{
		"email":    {email},
		"password": {"wrong-password"},
	}, nil)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for wrong password, got %d: %s", w.Code, w.Body.String())
	}
}

func TestLogin_HappyPath(t *testing.T) {
	cleanAccounts(t)
	r := newFullRouter()
	email := uniqueEmail()
	registerAndLogin(t, r, email, "correct-password")

	w := doFormRequest(t, r, http.MethodPost, "/login", url.Values{
		"email":    {email},
		"password": {"correct-password"},
	}, nil)
	if w.Code != http.StatusFound {
		t.Fatalf("expected login to redirect, got %d: %s", w.Code, w.Body.String())
	}
	if len(w.Result().Cookies()) == 0 {
		t.Fatal("expected a session cookie after login")
	}
}

func TestLogout_ClearsSession(t *testing.T) {
	cleanAccounts(t)
	r := newFullRouter()
	cookies := registerAndLogin(t, r, uniqueEmail(), "hunter2")

	w := doFormRequest(t, r, http.MethodPost, "/logout", nil, cookies)
	if w.Code != http.StatusFound {
		t.Fatalf("expected logout to redirect, got %d: %s", w.Code, w.Body.String())
	}

	// The logout response's Set-Cookie clears the session - using that fresh
	// (now-empty) cookie to hit a protected route should bounce to /login.
	freshCookies := w.Result().Cookies()
	profileW := doFormRequest(t, r, http.MethodGet, "/profile", nil, freshCookies)
	if profileW.Code != http.StatusFound {
		t.Fatalf("expected /profile to redirect after logout, got %d", profileW.Code)
	}
}

func TestProfile_RequiresLogin(t *testing.T) {
	r := newFullRouter()
	w := doFormRequest(t, r, http.MethodGet, "/profile", nil, nil)
	if w.Code != http.StatusFound {
		t.Fatalf("expected redirect to /login for anonymous /profile, got %d", w.Code)
	}
	loc := w.Header().Get("Location")
	if loc == "" || loc[:6] != "/login" {
		t.Fatalf("expected redirect to /login, got %q", loc)
	}
}

func TestAdminMarkets_RejectsNonAdmin(t *testing.T) {
	cleanAccounts(t)
	r := newFullRouter()
	// No INITIAL_ADMIN_EMAIL set, so nobody auto-becomes admin (see
	// store.CreateAccount) - both accounts here are plain non-admins.
	cookies := registerAndLogin(t, r, uniqueEmail(), "hunter2")

	w := doFormRequest(t, r, http.MethodGet, "/admin/markets", nil, cookies)
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for non-admin accessing /admin/markets, got %d: %s", w.Code, w.Body.String())
	}
}
