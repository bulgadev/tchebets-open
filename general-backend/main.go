package main

import (
	"log"
	"net/http"
	"os"
	"strings"

	"tchebet/general-backend/auth"
	"tchebet/general-backend/betclient"
	"tchebet/general-backend/handlers"
	"tchebet/general-backend/store"
	"tchebet/general-backend/walletclient"

	"github.com/gin-contrib/sessions"
	"github.com/gin-contrib/sessions/cookie"
	"github.com/gin-gonic/gin"
)

func main() {
	log.Println("Starting Tchebet General Backend...")

	// Phase 1/1.2 had no state here on purpose. Phase 2 needs accounts
	// (email/password/admin flag), which don't belong on bet-backend's
	// balance-only users table, so general-backend gets its own DB now.
	if _, err := store.InitDB(); err != nil {
		log.Fatalf("Failed to initialize general-backend database: %v", err)
	}

	bc := betclient.New()

	// Deposit mode flag: "fake" (default) keeps the R$ fake-money top-up;
	// "crypto" routes deposits/withdrawals through wallet-backend (real devnet
	// USDC). wc stays nil in fake mode - handlers branch on it.
	depositMode := getEnv("DEPOSIT_MODE", "fake")
	var wc *walletclient.Client
	if depositMode == "crypto" {
		wc = walletclient.New(getEnv("WALLET_BACKEND_URL", "http://wallet-backend:8082"))
		log.Println("Deposit mode: CRYPTO (wallet-backend)")
	} else {
		log.Println("Deposit mode: FAKE (R$ top-up)")
	}

	r := gin.Default()

	// Force HTTPS behind the Cloudflare Tunnel. The app always sees plain HTTP
	// from cloudflared, but the browser may be on http:// (Cloudflare isn't
	// forcing HTTPS at the edge). Since the session cookie is Secure, an http
	// visit makes the browser drop it and login silently fails. Cloudflare
	// reports the visitor's real scheme in CF-Visitor; if it was http, 301 to
	// https so the Secure cookie sticks. Loop-safe (https visits say
	// scheme:https) and a no-op when the header is absent (local dev / tests).
	r.Use(func(c *gin.Context) {
		if strings.Contains(c.GetHeader("CF-Visitor"), `"scheme":"http"`) {
			c.Redirect(http.StatusMovedPermanently, "https://"+c.Request.Host+c.Request.RequestURI)
			c.Abort()
			return
		}
		c.Next()
	})

	sessionSecret := getEnv("SESSION_SECRET", "dev-only-secret-change-me")
	sessionStore := cookie.NewStore([]byte(sessionSecret))
	// Harden the session cookie. COOKIE_SECURE=true (set it for the public HTTPS
	// deployment behind the Cloudflare Tunnel) marks the cookie Secure so it's
	// only ever sent over HTTPS; leave it unset for local http://localhost dev,
	// where a Secure cookie would never be stored and log-in would appear broken.
	sessionStore.Options(sessions.Options{
		Path:     "/",
		HttpOnly: true,
		Secure:   getEnv("COOKIE_SECURE", "false") == "true",
		SameSite: http.SameSiteLaxMode,
		MaxAge:   60 * 60 * 24 * 7, // 7 days
	})
	r.Use(sessions.Sessions("tchebet_session", sessionStore))

	r.Static("/static", "./static")

	// Existing + new JSON API surface (unauthenticated, unprefixed) - the
	// wrapper contract Phase 1.2 established, still targeted directly by
	// tests/integration_test.go.
	handlers.RegisterRoutes(r, bc)

	// Public pages - browsing needs no login. GET /markets/:id is registered
	// above, inside RegisterRoutes (content-negotiated with the JSON API).
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
		authed.GET("/profile", handlers.Profile(bc, wc))
		if wc != nil {
			// Crypto mode: passive deposit (send to the shown address) + a saque
			// form + the htmx polling partial that reflects a confirmed deposit.
			authed.POST("/profile/deposit-address", handlers.ProvisionDepositAddress(wc))
			authed.POST("/profile/withdraw", handlers.Withdraw(bc, wc))
			authed.POST("/profile/faucet", handlers.Faucet(wc))
			authed.GET("/profile/crypto-status", handlers.CryptoStatus(bc, wc))
		} else {
			authed.POST("/profile/deposit", handlers.Deposit(bc))
		}
		// A distinct path from the raw JSON POST /markets/:id/bet (Phase 1.2,
		// unauthenticated, user_id in the body) - this one is the Htmx form
		// post from a logged-in browser, user id comes from the session.
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

	port := getEnv("PORT", "8081")

	log.Printf("Tchebet General Backend running on port %s...", port)
	if err := r.Run(":" + port); err != nil {
		log.Fatalf("Server failed to run: %v", err)
	}
}

func getEnv(key, fallback string) string {
	if value, exists := os.LookupEnv(key); exists {
		return value
	}
	return fallback
}
