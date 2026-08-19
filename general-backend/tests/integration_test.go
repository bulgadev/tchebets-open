// Package tests exercises the real gin router wired to a real betclient
// pointed at a live bet-backend - the sanitize package already has its own
// fast unit tests, this file is only for the parts that need the network.
//
// newRouter() below only wires the JSON API surface (Phase 1.2's original
// scope). auth_test.go/admin_test.go/frontend_test.go need the full app -
// sessions, general-backend's own DB, the page routes - so they use
// newFullRouter() instead, defined in this file since TestMain (which
// initializes the DB connection they all share) can only live once per
// package.
package tests

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"tchebet/general-backend/auth"
	"tchebet/general-backend/betclient"
	"tchebet/general-backend/handlers"
	"tchebet/general-backend/store"

	"github.com/gin-contrib/sessions"
	"github.com/gin-contrib/sessions/cookie"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func TestMain(m *testing.M) {
	// Fallback for the rare case someone runs this against a manually
	// published bet-backend port instead of through compose.
	if os.Getenv("BET_BACKEND_URL") == "" {
		os.Setenv("BET_BACKEND_URL", "http://localhost:8080")
	}
	if os.Getenv("DB_HOST") == "" {
		os.Setenv("DB_HOST", "localhost")
	}
	if os.Getenv("DB_NAME") == "" {
		os.Setenv("DB_NAME", "tchebet_general")
	}
	gin.SetMode(gin.TestMode)

	if _, err := store.InitDB(); err != nil {
		panic("failed to initialize general-backend test database: " + err.Error())
	}

	os.Exit(m.Run())
}

func newRouter() *gin.Engine {
	r := gin.New()
	handlers.RegisterRoutes(r, betclient.New())
	return r
}

// newFullRouter wires the whole app - sessions, JSON API, and every page
// route - the same way main.go does, for tests that need auth/admin/page
// rendering rather than just the raw API surface.
func newFullRouter() *gin.Engine {
	bc := betclient.New()
	r := gin.New()

	sessionStore := cookie.NewStore([]byte("test-secret"))
	r.Use(sessions.Sessions("tchebet_session", sessionStore))

	handlers.RegisterRoutes(r, bc)

	r.GET("/", handlers.Home(bc))
	r.GET("/partials/markets", handlers.MarketsPartial(bc))
	r.GET("/register", handlers.RegisterPage)
	r.POST("/register", handlers.Register(bc))
	r.GET("/login", handlers.LoginPage)
	r.POST("/login", handlers.Login)

	authed := r.Group("/")
	authed.Use(auth.RequireUser())
	{
		authed.POST("/logout", handlers.Logout)
		// nil walletclient == fake deposit mode (the suite covers fake-money flows).
		authed.GET("/profile", handlers.Profile(bc, nil))
		authed.POST("/profile/deposit", handlers.Deposit(bc))
		authed.POST("/markets/:id/place-bet", handlers.PlaceBetPage(bc))
		authed.POST("/list-bet/:id", handlers.ListBetForSalePage(bc))
		authed.POST("/cancel-listing/:id", handlers.CancelListingPage(bc))
		authed.POST("/buy-listing/:id", handlers.BuyListingPage(bc))
	}

	admin := r.Group("/admin")
	admin.Use(auth.RequireUser(), auth.RequireAdmin())
	{
		admin.GET("/markets", handlers.AdminMarkets(bc))
		admin.GET("/markets/new", handlers.AdminNewMarketPageHandler(bc))
		admin.POST("/markets", handlers.AdminCreateMarket(bc))
		admin.POST("/markets/:id/lock", handlers.AdminLockMarket(bc))
		admin.POST("/markets/:id/resolve", handlers.AdminResolveMarket(bc))
		admin.POST("/markets/:id/cancel", handlers.AdminCancelMarket(bc))
		admin.GET("/users", handlers.AdminUsers(bc))
		admin.POST("/users/promote", handlers.AdminPromoteUser(bc))
	}

	return r
}

// cleanAccounts wipes general-backend's own accounts table between tests -
// mirrors bet-backend's tests/integration_test.go cleanTables helper.
func cleanAccounts(t *testing.T) {
	t.Helper()
	if _, err := store.DB.Exec(`DELETE FROM accounts`); err != nil {
		t.Fatal(err)
	}
}

func doRequest(t *testing.T, r *gin.Engine, method, path string, body interface{}) *httptest.ResponseRecorder {
	t.Helper()

	var reqBody *bytes.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("failed to marshal request body: %v", err)
		}
		reqBody = bytes.NewReader(b)
	} else {
		reqBody = bytes.NewReader(nil)
	}

	req := httptest.NewRequest(method, path, reqBody)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func decodeJSON(t *testing.T, w *httptest.ResponseRecorder) map[string]interface{} {
	t.Helper()
	var out map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("failed to decode response body %q: %v", w.Body.String(), err)
	}
	return out
}

func syncUser(t *testing.T, r *gin.Engine, balance int64) string {
	t.Helper()
	id := uuid.New().String()
	w := doRequest(t, r, http.MethodPost, "/users/sync", map[string]interface{}{
		"id":      id,
		"balance": balance,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("expected sync to succeed, got %d: %s", w.Code, w.Body.String())
	}
	return id
}

func createMarket(t *testing.T, r *gin.Engine, outcomes []string, seed int64) string {
	t.Helper()
	w := doRequest(t, r, http.MethodPost, "/markets", map[string]interface{}{
		"question": "Who wins?",
		"outcomes": outcomes,
		"seed":     seed,
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("expected market creation to succeed, got %d: %s", w.Code, w.Body.String())
	}
	body := decodeJSON(t, w)
	id, ok := body["id"].(string)
	if !ok {
		t.Fatalf("expected market response to contain an id, got %v", body)
	}
	return id
}

func TestSyncUser_HappyPath(t *testing.T) {
	r := newRouter()
	id := syncUser(t, r, 1000)
	if id == "" {
		t.Fatal("expected a user id")
	}
}

func TestSyncUser_RejectsMalformedUUID(t *testing.T) {
	r := newRouter()
	w := doRequest(t, r, http.MethodPost, "/users/sync", map[string]interface{}{
		"id":      "not-a-uuid",
		"balance": 100,
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for malformed uuid, got %d: %s", w.Code, w.Body.String())
	}
}

func TestSyncUser_RejectsNegativeBalance(t *testing.T) {
	r := newRouter()
	w := doRequest(t, r, http.MethodPost, "/users/sync", map[string]interface{}{
		"id":      uuid.New().String(),
		"balance": -1,
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for negative balance, got %d: %s", w.Code, w.Body.String())
	}
}

func TestCreateMarket_HappyPath(t *testing.T) {
	r := newRouter()
	id := createMarket(t, r, []string{"A", "B"}, 50)
	if id == "" {
		t.Fatal("expected a market id")
	}
}

func TestCreateMarket_RejectsDuplicateOutcomes(t *testing.T) {
	r := newRouter()
	w := doRequest(t, r, http.MethodPost, "/markets", map[string]interface{}{
		"question": "Who wins?",
		"outcomes": []string{"A", "a"},
		"seed":     10,
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for duplicate outcomes, got %d: %s", w.Code, w.Body.String())
	}
}

func TestCreateMarket_RejectsTooFewOutcomes(t *testing.T) {
	r := newRouter()
	w := doRequest(t, r, http.MethodPost, "/markets", map[string]interface{}{
		"question": "Who wins?",
		"outcomes": []string{"A"},
		"seed":     10,
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for too few outcomes, got %d: %s", w.Code, w.Body.String())
	}
}

func TestCreateMarket_RejectsTooManyOutcomes(t *testing.T) {
	r := newRouter()
	outcomes := make([]string, 11)
	for i := range outcomes {
		outcomes[i] = uuid.New().String()
	}
	w := doRequest(t, r, http.MethodPost, "/markets", map[string]interface{}{
		"question": "Who wins?",
		"outcomes": outcomes,
		"seed":     10,
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for too many outcomes, got %d: %s", w.Code, w.Body.String())
	}
}

func TestGetMarket_HappyPath(t *testing.T) {
	r := newRouter()
	id := createMarket(t, r, []string{"A", "B"}, 50)

	w := doRequest(t, r, http.MethodGet, "/markets/"+id, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	body := decodeJSON(t, w)
	if body["status"] != "OPEN" {
		t.Fatalf("expected freshly created market to be OPEN, got %v", body["status"])
	}
}

func TestGetMarket_RejectsMalformedUUID(t *testing.T) {
	r := newRouter()
	w := doRequest(t, r, http.MethodGet, "/markets/not-a-uuid", nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for malformed uuid, got %d: %s", w.Code, w.Body.String())
	}
}

func TestGetMarket_NotFound(t *testing.T) {
	r := newRouter()
	w := doRequest(t, r, http.MethodGet, "/markets/"+uuid.New().String(), nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 relayed from bet-backend, got %d: %s", w.Code, w.Body.String())
	}
}

func TestPlaceBet_HappyPath(t *testing.T) {
	r := newRouter()
	userID := syncUser(t, r, 1000)
	marketID := createMarket(t, r, []string{"A", "B"}, 50)

	w := doRequest(t, r, http.MethodPost, "/markets/"+marketID+"/bet", map[string]interface{}{
		"user_id": userID,
		"outcome": "A",
		"stake":   100,
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	getW := doRequest(t, r, http.MethodGet, "/markets/"+marketID, nil)
	body := decodeJSON(t, getW)
	pools, ok := body["pools"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected pools in market details, got %v", body)
	}
	if pools["A"].(float64) != 150 { // seed 50 + stake 100
		t.Fatalf("expected pool A to reflect the bet, got %v", pools["A"])
	}
}

func TestPlaceBet_RejectsMalformedUserID(t *testing.T) {
	r := newRouter()
	marketID := createMarket(t, r, []string{"A", "B"}, 50)

	w := doRequest(t, r, http.MethodPost, "/markets/"+marketID+"/bet", map[string]interface{}{
		"user_id": "not-a-uuid",
		"outcome": "A",
		"stake":   100,
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for malformed user id, got %d: %s", w.Code, w.Body.String())
	}
}

func TestPlaceBet_RejectsNonPositiveStake(t *testing.T) {
	r := newRouter()
	userID := syncUser(t, r, 1000)
	marketID := createMarket(t, r, []string{"A", "B"}, 50)

	w := doRequest(t, r, http.MethodPost, "/markets/"+marketID+"/bet", map[string]interface{}{
		"user_id": userID,
		"outcome": "A",
		"stake":   0,
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for zero stake, got %d: %s", w.Code, w.Body.String())
	}
}

func TestPlaceBet_RelaysInvalidOutcome(t *testing.T) {
	r := newRouter()
	userID := syncUser(t, r, 1000)
	marketID := createMarket(t, r, []string{"A", "B"}, 50)

	w := doRequest(t, r, http.MethodPost, "/markets/"+marketID+"/bet", map[string]interface{}{
		"user_id": userID,
		"outcome": "C",
		"stake":   100,
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 relayed for invalid outcome, got %d: %s", w.Code, w.Body.String())
	}
}

func TestPlaceBet_RelaysInsufficientBalance(t *testing.T) {
	r := newRouter()
	userID := syncUser(t, r, 0)
	marketID := createMarket(t, r, []string{"A", "B"}, 50)

	w := doRequest(t, r, http.MethodPost, "/markets/"+marketID+"/bet", map[string]interface{}{
		"user_id": userID,
		"outcome": "A",
		"stake":   100,
	})
	if w.Code != http.StatusPaymentRequired {
		t.Fatalf("expected 402 relayed from bet-backend, got %d: %s", w.Code, w.Body.String())
	}
}

func TestLockMarket_ThenBetIsRejected(t *testing.T) {
	r := newRouter()
	userID := syncUser(t, r, 1000)
	marketID := createMarket(t, r, []string{"A", "B"}, 50)

	lockW := doRequest(t, r, http.MethodPost, "/markets/"+marketID+"/lock", nil)
	if lockW.Code != http.StatusOK {
		t.Fatalf("expected lock to succeed, got %d: %s", lockW.Code, lockW.Body.String())
	}

	betW := doRequest(t, r, http.MethodPost, "/markets/"+marketID+"/bet", map[string]interface{}{
		"user_id": userID,
		"outcome": "A",
		"stake":   100,
	})
	if betW.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for betting on a locked market, got %d: %s", betW.Code, betW.Body.String())
	}
}

func TestResolveMarket_Flow(t *testing.T) {
	r := newRouter()
	marketID := createMarket(t, r, []string{"A", "B"}, 50)

	lockW := doRequest(t, r, http.MethodPost, "/markets/"+marketID+"/lock", nil)
	if lockW.Code != http.StatusOK {
		t.Fatalf("expected lock to succeed, got %d: %s", lockW.Code, lockW.Body.String())
	}

	resolveW := doRequest(t, r, http.MethodPost, "/markets/"+marketID+"/resolve", map[string]interface{}{
		"winning_outcome": "A",
	})
	if resolveW.Code != http.StatusOK {
		t.Fatalf("expected resolve to succeed, got %d: %s", resolveW.Code, resolveW.Body.String())
	}

	getW := doRequest(t, r, http.MethodGet, "/markets/"+marketID, nil)
	body := decodeJSON(t, getW)
	if body["status"] != "RESOLVED" {
		t.Fatalf("expected market to be RESOLVED, got %v", body["status"])
	}
	if body["winning_outcome"] != "A" {
		t.Fatalf("expected winning_outcome A, got %v", body["winning_outcome"])
	}

	// Idempotency: resolving an already-resolved market must not succeed a second time.
	secondResolveW := doRequest(t, r, http.MethodPost, "/markets/"+marketID+"/resolve", map[string]interface{}{
		"winning_outcome": "A",
	})
	if secondResolveW.Code == http.StatusOK {
		t.Fatalf("expected re-resolving a resolved market to fail, got %d: %s", secondResolveW.Code, secondResolveW.Body.String())
	}
}

func TestResolveMarket_RejectsMalformedOutcome(t *testing.T) {
	r := newRouter()
	marketID := createMarket(t, r, []string{"A", "B"}, 50)
	lockW := doRequest(t, r, http.MethodPost, "/markets/"+marketID+"/lock", nil)
	if lockW.Code != http.StatusOK {
		t.Fatalf("expected lock to succeed, got %d: %s", lockW.Code, lockW.Body.String())
	}

	w := doRequest(t, r, http.MethodPost, "/markets/"+marketID+"/resolve", map[string]interface{}{
		"winning_outcome": "   ",
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for blank winning_outcome, got %d: %s", w.Code, w.Body.String())
	}
}

func TestCancelMarket_Flow(t *testing.T) {
	r := newRouter()
	marketID := createMarket(t, r, []string{"A", "B"}, 50)

	cancelW := doRequest(t, r, http.MethodPost, "/markets/"+marketID+"/cancel", nil)
	if cancelW.Code != http.StatusOK {
		t.Fatalf("expected cancel to succeed, got %d: %s", cancelW.Code, cancelW.Body.String())
	}

	getW := doRequest(t, r, http.MethodGet, "/markets/"+marketID, nil)
	body := decodeJSON(t, getW)
	if body["status"] != "CANCELLED" {
		t.Fatalf("expected market to be CANCELLED, got %v", body["status"])
	}
}