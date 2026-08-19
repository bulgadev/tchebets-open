package handlers

import (
	"errors"
	"net/http"

	"tchebet/general-backend/betclient"
	"tchebet/general-backend/sanitize"

	"github.com/gin-gonic/gin"
)

const (
	maxQuestionLen = 500
	// (Is this the actual max output considering bets could be of different sizes?)
	maxOutcomeLen = 100 // matches bet-backend's outcome VARCHAR(100) column
	minOutcomes   = 2
	maxOutcomes   = 10
	maxEmailLen   = 254 // RFC 5321 practical max
	maxTeamLen    = 60  // admin team-picker names, see Teams in teams.go
)

// RegisterRoutes wires the frontend-facing routes 1:1 to bet-backend's own
// route surface. Every one of these exists because bet-backend won't take
// traffic straight from the frontend anymore - including lock/resolve/cancel,
// which are admin-only in spirit but need to already be here so Phase 2 can
// just add auth middleware instead of inventing new routes. 

// (Have in mind general backend will need all the frontend stuff to, and phase 2 includes the frontend and general prototype)
func RegisterRoutes(r *gin.Engine, bc *betclient.Client) {
	r.POST("/users/sync", syncUser(bc))
	r.GET("/users/:id", getUser(bc))
	r.POST("/markets", createMarket(bc))
	r.GET("/markets", listMarkets(bc))
	// GET /markets/:id is shared by the JSON API (Phase 1.2's test suite hits
	// it directly, no Accept header) and the browser-facing market page - one
	// route, content-negotiated in marketDetailDispatch (public_handlers.go),
	// rather than two handlers fighting over the same path.
	r.GET("/markets/:id", marketDetailDispatch(bc))
	r.GET("/markets/:id/bets", listMarketBets(bc))
	r.POST("/markets/:id/bet", placeBet(bc))
	r.POST("/markets/:id/lock", lockMarket(bc))
	r.POST("/markets/:id/resolve", resolveMarket(bc))
	r.POST("/markets/:id/cancel", cancelMarket(bc))
	r.GET("/users/:id/bets", listUserBets(bc))
	r.POST("/bets/:id/listings", createListing(bc))
	r.POST("/listings/:id/cancel", cancelListing(bc))
	r.POST("/listings/:id/buy", buyListing(bc))
	r.GET("/markets/:id/listings", listMarketListings(bc))
	r.GET("/users/:id/listings", listUserListings(bc))
}

// relay writes bet-backend's response straight through, or a 502 if
// bet-backend couldn't be reached at all.
func relay(c *gin.Context, resp *betclient.Response, err error) {
	if err != nil {
		if errors.Is(err, betclient.ErrUnavailable) {
			c.JSON(http.StatusBadGateway, gin.H{"error": "betting engine unavailable"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.Data(resp.StatusCode, "application/json; charset=utf-8", resp.Body)
}

type syncUserRequest struct {
	ID string `json:"id" binding:"required"`
	// No "required" on Balance - gin's validator treats the int64 zero value
	// as "missing", which would wrongly reject a brand new user starting at
	// balance 0. sanitize.NonNegativeInt64 below is the real check.
	Balance int64 `json:"balance"`
}

func syncUser(bc *betclient.Client) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req syncUserRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		id, err := sanitize.UUID(req.ID)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		balance, err := sanitize.NonNegativeInt64(req.Balance, sanitize.MaxSanityInt)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		resp, err := bc.SyncUser(c.Request.Context(), id, balance)
		relay(c, resp, err)
	}
}

func getUser(bc *betclient.Client) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, err := sanitize.UUID(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		resp, err := bc.GetUserRaw(c.Request.Context(), id)
		relay(c, resp, err)
	}
}

type createMarketRequest struct {
	Question string   `json:"question" binding:"required"`
	Outcomes []string `json:"outcomes" binding:"required"`
	Seed     int64    `json:"seed"`
}

func createMarket(bc *betclient.Client) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req createMarketRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		question, err := sanitize.NonEmptyString(req.Question, maxQuestionLen)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		outcomes, err := sanitize.Outcomes(req.Outcomes, minOutcomes, maxOutcomes, maxOutcomeLen)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		seed, err := sanitize.NonNegativeInt64(req.Seed, sanitize.MaxSanityInt)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		resp, err := bc.CreateMarket(c.Request.Context(), question, outcomes, seed)
		relay(c, resp, err)
	}
}

func getMarket(bc *betclient.Client) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, err := sanitize.UUID(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		resp, err := bc.GetMarket(c.Request.Context(), id)
		relay(c, resp, err)
	}
}

func listMarkets(bc *betclient.Client) gin.HandlerFunc {
	return func(c *gin.Context) {
		resp, err := bc.ListMarketsRaw(c.Request.Context())
		relay(c, resp, err)
	}
}

func listMarketBets(bc *betclient.Client) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, err := sanitize.UUID(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		resp, err := bc.ListMarketBetsRaw(c.Request.Context(), id)
		relay(c, resp, err)
	}
}

func listUserBets(bc *betclient.Client) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, err := sanitize.UUID(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		resp, err := bc.ListUserBetsRaw(c.Request.Context(), id)
		relay(c, resp, err)
	}
}

type placeBetRequest struct {
	UserID  string `json:"user_id" binding:"required"`
	Outcome string `json:"outcome" binding:"required"`
	Stake   int64  `json:"stake" binding:"required"`
}

func placeBet(bc *betclient.Client) gin.HandlerFunc {
	return func(c *gin.Context) {
		marketID, err := sanitize.UUID(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		var req placeBetRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		userID, err := sanitize.UUID(req.UserID)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		outcome, err := sanitize.NonEmptyString(req.Outcome, maxOutcomeLen)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		stake, err := sanitize.PositiveInt64(req.Stake, sanitize.MaxSanityInt)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		resp, err := bc.PlaceBet(c.Request.Context(), marketID, userID, outcome, stake)
		relay(c, resp, err)
	}
}

func lockMarket(bc *betclient.Client) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, err := sanitize.UUID(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		resp, err := bc.LockMarket(c.Request.Context(), id)
		relay(c, resp, err)
	}
}

type resolveMarketRequest struct {
	WinningOutcome string `json:"winning_outcome" binding:"required"`
}

func resolveMarket(bc *betclient.Client) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, err := sanitize.UUID(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		var req resolveMarketRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		winningOutcome, err := sanitize.NonEmptyString(req.WinningOutcome, maxOutcomeLen)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		resp, err := bc.ResolveMarket(c.Request.Context(), id, winningOutcome)
		relay(c, resp, err)
	}
}

func cancelMarket(bc *betclient.Client) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, err := sanitize.UUID(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		resp, err := bc.CancelMarket(c.Request.Context(), id)
		relay(c, resp, err)
	}
}

type createListingRequest struct {
	SellerID    string `json:"seller_id" binding:"required"`
	AskingPrice int64  `json:"asking_price" binding:"required"`
}

func createListing(bc *betclient.Client) gin.HandlerFunc {
	return func(c *gin.Context) {
		betID, err := sanitize.UUID(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		var req createListingRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		sellerID, err := sanitize.UUID(req.SellerID)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		askingPrice, err := sanitize.PositiveInt64(req.AskingPrice, sanitize.MaxSanityInt)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		resp, err := bc.ListBetForSale(c.Request.Context(), betID, sellerID, askingPrice)
		relay(c, resp, err)
	}
}

type cancelListingRequest struct {
	SellerID string `json:"seller_id" binding:"required"`
}

func cancelListing(bc *betclient.Client) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, err := sanitize.UUID(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		var req cancelListingRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		sellerID, err := sanitize.UUID(req.SellerID)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		resp, err := bc.CancelListing(c.Request.Context(), id, sellerID)
		relay(c, resp, err)
	}
}

type buyListingRequest struct {
	BuyerID string `json:"buyer_id" binding:"required"`
}

func buyListing(bc *betclient.Client) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, err := sanitize.UUID(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		var req buyListingRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		buyerID, err := sanitize.UUID(req.BuyerID)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		resp, err := bc.BuyListing(c.Request.Context(), id, buyerID)
		relay(c, resp, err)
	}
}

func listMarketListings(bc *betclient.Client) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, err := sanitize.UUID(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		resp, err := bc.ListMarketListingsRaw(c.Request.Context(), id)
		relay(c, resp, err)
	}
}

func listUserListings(bc *betclient.Client) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, err := sanitize.UUID(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		resp, err := bc.ListUserListingsRaw(c.Request.Context(), id)
		relay(c, resp, err)
	}
}