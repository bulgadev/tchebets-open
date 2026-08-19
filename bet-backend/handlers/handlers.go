package handlers

import (
	"errors"
	"net/http"

	"tchebet/bet-backend/db"
	"tchebet/bet-backend/service"

	"github.com/gin-gonic/gin"
)

// RegisterRoutes registers all endpoints on the gin engine
// Fine as long as this doesnt become a giant amounts of routes. If more then 20 should probably become a whole new dedicated  file
func RegisterRoutes(r *gin.Engine) {
	r.POST("/users/sync", SyncUser)
	r.GET("/users/:id", GetUser)
	r.POST("/users/:id/adjust", AdjustBalance)
	r.POST("/markets", CreateMarket)
	r.GET("/markets", ListMarkets)
	r.GET("/markets/:id", GetMarket)
	r.GET("/markets/:id/bets", ListMarketBets)
	r.POST("/markets/:id/bet", PlaceBet)
	r.POST("/markets/:id/lock", LockMarket)
	r.POST("/markets/:id/resolve", ResolveMarket)
	r.POST("/markets/:id/cancel", CancelMarket)
	r.GET("/users/:id/bets", ListUserBets)
}

// Maybe the actions below could be abstracted in a object in a better way? Idk seems like a lot of the same
// This file basically acts as a object so ig its fine.

type SyncUserRequest struct {
	ID string `json:"id" binding:"required,uuid"`
	// No "required" here - gin's validator treats the int64 zero value as
	// "missing", which would wrongly reject a brand new user starting at
	// balance 0. min=0 is the real check.
	Balance int64 `json:"balance" binding:"min=0"`
}

func SyncUser(c *gin.Context) {
	var req SyncUserRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	query := `
		INSERT INTO users (id, balance, created_at)
		VALUES ($1, $2, NOW())
		ON CONFLICT (id) DO UPDATE SET balance = EXCLUDED.balance
	`
	_, err := db.DB.Exec(query, req.ID, req.Balance)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to sync user: " + err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "success", "user_id": req.ID, "balance": req.Balance})
}

// GetUser reads a user's current balance - separate from SyncUser (an
// upsert) so callers can check a balance without accidentally overwriting it.
func GetUser(c *gin.Context) {
	id := c.Param("id")
	user, err := service.GetUser(id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, user)
}

// AdjustBalanceRequest carries a signed delta. Negative debits (withdrawals),
// positive credits (deposits). No "required" on Delta - a caller must be able
// to send any int64, and gin's validator treats 0 as missing anyway; a 0 delta
// is simply a no-op adjust.
type AdjustBalanceRequest struct {
	Delta int64 `json:"delta"`
}

// AdjustBalance atomically moves a user's balance by delta - the safe primitive
// wallet-backend uses to credit deposits and debit withdrawals (see
// service.AdjustBalance for why SyncUser's setter can't do this without a race).
func AdjustBalance(c *gin.Context) {
	id := c.Param("id")
	var req AdjustBalanceRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	newBalance, err := service.AdjustBalance(id, req.Delta)
	if err != nil {
		if errors.Is(err, service.ErrInsufficientBalance) {
			c.JSON(http.StatusPaymentRequired, gin.H{"error": err.Error()})
			return
		}
		if err.Error() == "user not found" {
			c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "success", "user_id": id, "balance": newBalance})
}

type CreateMarketRequest struct {
	Question string   `json:"question" binding:"required"`
	Outcomes []string `json:"outcomes" binding:"required,min=2"`
	Seed     int64    `json:"seed" binding:"min=0"`
}

func CreateMarket(c *gin.Context) {
	var req CreateMarketRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	market, err := service.CreateMarket(req.Question, req.Outcomes, req.Seed)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusCreated, market)
}

func GetMarket(c *gin.Context) {
	id := c.Param("id")
	details, err := service.GetMarketDetails(id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, details)
}

// ListMarkets powers the Phase 2 home page - every market with its current
// pools/live odds, newest first.
func ListMarkets(c *gin.Context) {
	markets, err := service.ListMarkets()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, markets)
}

// ListMarketBets powers both the market page's activity feed and its
// probability-history sparkline (general-backend replays these bets to
// reconstruct pool history over time) - includes house seed bets.
func ListMarketBets(c *gin.Context) {
	marketID := c.Param("id")
	bets, err := service.ListMarketBets(marketID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, bets)
}

type PlaceBetRequest struct {
	UserID  string `json:"user_id" binding:"required,uuid"`
	Outcome string `json:"outcome" binding:"required"`
	Stake   int64  `json:"stake" binding:"required,gt=0"`
}

func PlaceBet(c *gin.Context) {
	marketID := c.Param("id")
	var req PlaceBetRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	bet, err := service.PlaceBet(req.UserID, marketID, req.Outcome, req.Stake)
	if err != nil { // good error handling
		if errors.Is(err, service.ErrInsufficientBalance) {
			c.JSON(http.StatusPaymentRequired, gin.H{"error": err.Error()})
			return
		}
		if errors.Is(err, service.ErrMarketNotOpen) || errors.Is(err, service.ErrInvalidOutcome) {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusCreated, bet)
}

func LockMarket(c *gin.Context) {
	id := c.Param("id")
	err := service.LockMarket(id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "success", "message": "market locked"})
}

type ResolveMarketRequest struct {
	WinningOutcome string `json:"winning_outcome" binding:"required"`
}

func ResolveMarket(c *gin.Context) {
	id := c.Param("id")
	var req ResolveMarketRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	err := service.ResolveMarket(id, req.WinningOutcome)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "success", "message": "market resolved"})
}

func CancelMarket(c *gin.Context) {
	id := c.Param("id")
	err := service.CancelMarket(id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "success", "message": "market cancelled"})
}

// ListUserBets powers the Phase 2 profile page's bet history.
func ListUserBets(c *gin.Context) {
	userID := c.Param("id")
	bets, err := service.ListUserBets(userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, bets)
}
