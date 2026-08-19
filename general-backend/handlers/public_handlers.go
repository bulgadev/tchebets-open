package handlers

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"tchebet/general-backend/auth"
	"tchebet/general-backend/betclient"
	"tchebet/general-backend/sanitize"
	"tchebet/general-backend/store"
	"tchebet/general-backend/views"

	"github.com/gin-gonic/gin"
)

func Home(bc *betclient.Client) gin.HandlerFunc {
	return func(c *gin.Context) {
		markets, cards, err := loadFilteredCards(c, bc, "todos")
		if err != nil {
			c.String(http.StatusBadGateway, "betting engine unavailable")
			return
		}

		nav := buildNav(c, bc)
		views.Home(nav, len(markets), cards).Render(c.Request.Context(), c.Writer)
	}
}

// MarketsPartial re-renders just the card grid for the home page's filter
// tabs (Htmx swap) - no full page reload needed to switch Todos/Hoje/Esta
// Semana.
func MarketsPartial(bc *betclient.Client) gin.HandlerFunc {
	return func(c *gin.Context) {
		filter := c.DefaultQuery("filter", "todos")
		_, cards, err := loadFilteredCards(c, bc, filter)
		if err != nil {
			c.String(http.StatusBadGateway, "betting engine unavailable")
			return
		}
		views.MarketGrid(cards).Render(c.Request.Context(), c.Writer)
	}
}

func loadFilteredCards(c *gin.Context, bc *betclient.Client, filter string) ([]betclient.MarketDetails, []views.MarketCardVM, error) {
	markets, err := bc.ListMarkets(c.Request.Context())
	if err != nil {
		return nil, nil, err
	}

	now := time.Now()
	var filtered []betclient.MarketDetails
	for _, m := range markets {
		if m.Status != "OPEN" {
			continue
		}
		switch filter {
		case "hoje":
			if m.CreatedAt.Year() != now.Year() || m.CreatedAt.YearDay() != now.YearDay() {
				continue
			}
		case "semana":
			if now.Sub(m.CreatedAt) > 7*24*time.Hour {
				continue
			}
		}
		filtered = append(filtered, m)
	}

	cards := make([]views.MarketCardVM, 0, len(filtered))
	for _, m := range filtered {
		cards = append(cards, buildMarketCardVM(m))
	}
	return filtered, cards, nil
}

// marketDetailDispatch is the single registration for GET /markets/:id -
// Phase 1.2's test suite (and any other API client) hits this path with no
// Accept header and expects the original raw JSON; a browser navigating to
// a market sends "Accept: text/html,...". One route, content-negotiated,
// rather than two handlers fighting over the same path.
func marketDetailDispatch(bc *betclient.Client) gin.HandlerFunc {
	jsonHandler := getMarket(bc)
	pageHandler := marketDetailPage(bc)
	return func(c *gin.Context) {
		if strings.Contains(c.GetHeader("Accept"), "text/html") {
			pageHandler(c)
			return
		}
		jsonHandler(c)
	}
}

func marketDetailPage(bc *betclient.Client) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, err := sanitize.UUID(c.Param("id"))
		if err != nil {
			c.String(http.StatusBadRequest, "invalid market id")
			return
		}

		details, err := bc.GetMarketDetails(c.Request.Context(), id)
		if err != nil {
			c.String(http.StatusNotFound, "market not found")
			return
		}

		bets, err := bc.ListMarketBets(c.Request.Context(), id)
		if err != nil {
			c.String(http.StatusBadGateway, "betting engine unavailable")
			return
		}

		listings, err := bc.ListMarketListings(c.Request.Context(), id)
		if err != nil {
			listings = nil // tolerate the listing engine being unreachable - the rest of the page still renders
		}

		accountsByID := loadAccountsFor(bets)
		mergeAccountsForListings(listings, accountsByID)
		viewerID, _ := auth.CurrentUserID(c)
		vm := buildMarketDetailVM(*details, bets, accountsByID, listings, viewerID)

		nav := buildNav(c, bc)
		views.MarketDetail(nav, vm).Render(c.Request.Context(), c.Writer)
	}
}

func loadAccountsFor(bets []betclient.Bet) map[string]*store.Account {
	out := map[string]*store.Account{}
	seen := map[string]bool{}
	for _, b := range bets {
		if seen[b.UserID] || b.UserID == houseUserID {
			continue
		}
		seen[b.UserID] = true
		if acct, err := store.FindAccountByID(b.UserID); err == nil {
			out[b.UserID] = acct
		}
	}
	return out
}

type placeBetFormRequest struct {
	Outcome string `form:"outcome" binding:"required"`
	Stake   int64  `form:"stake" binding:"required"`
}

// PlaceBetPage handles the Htmx bet-slip submission - sanitize, place the
// bet, then re-render the scoreboard/activity partial with fresh data
// straight from bet-backend rather than duplicating payout math here.
func PlaceBetPage(bc *betclient.Client) gin.HandlerFunc {
	return func(c *gin.Context) {
		marketID, err := sanitize.UUID(c.Param("id"))
		if err != nil {
			c.String(http.StatusBadRequest, "invalid market id")
			return
		}

		var req placeBetFormRequest
		if err := c.ShouldBind(&req); err != nil {
			c.String(http.StatusBadRequest, `<div class="text-tche-red text-sm">dados inválidos</div>`)
			return
		}

		outcome, err := sanitize.NonEmptyString(req.Outcome, maxOutcomeLen)
		if err != nil {
			c.String(http.StatusBadRequest, `<div class="text-tche-red text-sm">`+err.Error()+`</div>`)
			return
		}
		stake, err := sanitize.PositiveInt64(req.Stake, sanitize.MaxSanityInt)
		if err != nil {
			c.String(http.StatusBadRequest, `<div class="text-tche-red text-sm">`+err.Error()+`</div>`)
			return
		}

		userID, ok := auth.CurrentUserID(c)
		if !ok {
			c.String(http.StatusUnauthorized, `<div class="text-tche-red text-sm">faça login para apostar</div>`)
			return
		}

		if _, err := bc.PlaceBetTyped(c.Request.Context(), marketID, userID, outcome, stake); err != nil {
			msg := "não foi possível registrar a aposta"
			var se *betclient.StatusError
			if errors.As(err, &se) {
				msg = se.Message
			}
			c.String(http.StatusOK, `<div class="text-tche-red text-sm">`+msg+`</div>`)
			return
		}

		c.String(http.StatusOK, `<div class="text-tche-green text-sm">Aposta registrada! <a href="/markets/`+marketID+`" class="underline">Atualizar página</a></div>`)
	}
}
