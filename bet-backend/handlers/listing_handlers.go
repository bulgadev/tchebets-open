package handlers

import (
	"errors"
	"net/http"

	"tchebet/bet-backend/service"

	"github.com/gin-gonic/gin"
)

// RegisterListingRoutes wires the bet-resale (secondary market) endpoints -
// kept in a separate file/registration call from RegisterRoutes since that
// one is explicitly commented as "split into a new file past 20 routes".
func RegisterListingRoutes(r *gin.Engine) {
	r.POST("/bets/:id/listings", CreateListing)
	r.POST("/listings/:id/cancel", CancelListing)
	r.POST("/listings/:id/buy", BuyListing)
	r.GET("/markets/:id/listings", ListMarketListings)
	r.GET("/users/:id/listings", ListUserListings)
}

// writeListingError maps listing_service sentinel errors onto this
// codebase's status-code vocabulary: 400/402/404/409/500, extending it with
// 403 (ownership) and 409 (conflict) - new here, but natural minimal
// extensions of the existing 400/402/404/500 set (see docs/codebase_guide.md).
func writeListingError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrBetNotFound), errors.Is(err, service.ErrListingNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
	case errors.Is(err, service.ErrNotBetOwner), errors.Is(err, service.ErrNotListingOwner):
		c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
	case errors.Is(err, service.ErrCannotListHouseBet), errors.Is(err, service.ErrCannotBuyOwnListing), errors.Is(err, service.ErrMarketNotOpen):
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	case errors.Is(err, service.ErrInsufficientBalance):
		c.JSON(http.StatusPaymentRequired, gin.H{"error": err.Error()})
	case errors.Is(err, service.ErrBetAlreadyListed), errors.Is(err, service.ErrListingNotActive), errors.Is(err, service.ErrListingMarketNoLongerOpen):
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
	}
}

type createListingRequest struct {
	SellerID    string `json:"seller_id" binding:"required,uuid"`
	AskingPrice int64  `json:"asking_price" binding:"required,gt=0"`
}

func CreateListing(c *gin.Context) {
	betID := c.Param("id")
	var req createListingRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	listing, err := service.ListBetForSale(betID, req.SellerID, req.AskingPrice)
	if err != nil {
		writeListingError(c, err)
		return
	}

	c.JSON(http.StatusCreated, listing)
}

type cancelListingRequest struct {
	SellerID string `json:"seller_id" binding:"required,uuid"`
}

func CancelListing(c *gin.Context) {
	listingID := c.Param("id")
	var req cancelListingRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := service.CancelListing(listingID, req.SellerID); err != nil {
		writeListingError(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "success", "message": "listing cancelled"})
}

type buyListingRequest struct {
	BuyerID string `json:"buyer_id" binding:"required,uuid"`
}

func BuyListing(c *gin.Context) {
	listingID := c.Param("id")
	var req buyListingRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	listing, err := service.BuyListedBet(listingID, req.BuyerID)
	if err != nil {
		writeListingError(c, err)
		return
	}

	c.JSON(http.StatusOK, listing)
}

// ListMarketListings powers the market page's "for sale" section.
func ListMarketListings(c *gin.Context) {
	marketID := c.Param("id")
	listings, err := service.ListActiveListingsForMarket(marketID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, listings)
}

// ListUserListings powers the profile page's "already listed" state.
func ListUserListings(c *gin.Context) {
	userID := c.Param("id")
	listings, err := service.ListActiveListingsForSeller(userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, listings)
}
