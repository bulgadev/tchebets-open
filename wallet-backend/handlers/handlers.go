// Package handlers is wallet-backend's thin HTTP surface. It has no host port
// in compose: only general-backend calls it over the internal docker network.
// The handlers translate JSON requests into service calls and map the service's
// typed errors onto status codes, mirroring bet-backend's conventions
// (400 bad input, 402 insufficient balance, 404 not found, 500 otherwise); a
// withdrawal parked for manual review returns 202 Accepted.
package handlers

import (
	"errors"
	"log"
	"net/http"

	"github.com/gin-gonic/gin"

	"tchebet/wallet-backend/models"
	"tchebet/wallet-backend/service"
	"tchebet/wallet-backend/store"
)

// RegisterRoutes wires the wallet API. svc runs the deposit/withdrawal flows;
// st serves the read-only history/status queries the deposit page polls.
func RegisterRoutes(r *gin.Engine, svc *service.Service, st *store.Store) {
	r.GET("/health", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"status": "ok"}) })
	r.GET("/accounts/:id/address", getAddress(st))
	r.POST("/accounts/:id/provision", provision(svc))
	r.POST("/accounts/:id/faucet", faucet(svc))
	r.POST("/accounts/:id/withdraw", withdraw(svc))
	r.GET("/accounts/:id/deposits", listDeposits(st))
	r.GET("/accounts/:id/withdrawals", listWithdrawals(st))
}

// getAddress returns only an address that already exists in the custody store.
// Unlike provision, it never contacts Solana and is safe for a profile render.
func getAddress(st *store.Store) gin.HandlerFunc {
	return func(c *gin.Context) {
		addr, err := st.GetAddressByAccount(c.Request.Context(), c.Param("id"))
		switch {
		case err == nil:
			c.JSON(http.StatusOK, addressView(addr))
		case errors.Is(err, store.ErrNotFound):
			c.JSON(http.StatusNotFound, gin.H{"error": "deposit address not provisioned"})
		default:
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		}
	}
}

// provision returns (creating on first call) the account's Solana deposit
// address and pre-created USDC ATA.
func provision(svc *service.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")
		addr, err := svc.ProvisionAccount(c.Request.Context(), id)
		if err != nil {
			// Provisioning creates the USDC ATA, which the treasury must fund -
			// log the real cause (e.g. unfunded treasury) instead of swallowing it.
			log.Printf("provision account %s failed: %v", id, err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, addressView(addr))
	}
}

func addressView(addr *models.WalletAddress) gin.H {
	return gin.H{
		"account_id": addr.AccountID,
		"address":    addr.Address,
		"usdc_ata":   addr.USDCATA,
	}
}

// faucet sends the one-time devnet USDC demo grant to the account's deposit ATA
// (from treasury). 409 if the account already claimed; 200 with the on-chain
// signature otherwise. The grant is credited by the normal deposit poller, so
// the caller just needs to know it was dispatched.
func faucet(svc *service.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")
		sig, err := svc.FaucetTestUSDC(c.Request.Context(), id)
		switch {
		case err == nil:
			c.JSON(http.StatusOK, gin.H{"signature": sig})
		case errors.Is(err, service.ErrFaucetAlreadyClaimed):
			c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		default:
			log.Printf("faucet for account %s failed: %v", id, err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		}
	}
}

type withdrawRequest struct {
	DestAddress string `json:"dest_address" binding:"required"`
	AmountReais int64  `json:"amount_reais" binding:"required"`
}

// withdraw debits the ledger (in whole BRL) and pays out the equivalent USDC. A
// parked-for-review withdrawal is a 202, not an error - it's debited and queued.
func withdraw(svc *service.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")
		var req withdrawRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		w, err := svc.RequestWithdrawal(c.Request.Context(), id, req.DestAddress, req.AmountReais)
		switch {
		case err == nil:
			c.JSON(http.StatusOK, withdrawalView(w))
		case errors.Is(err, service.ErrManualReview):
			// Debited and parked for an operator - accepted, not settled.
			c.JSON(http.StatusAccepted, withdrawalView(w))
		case errors.Is(err, service.ErrAmountBelowMin), errors.Is(err, service.ErrAmountAboveMax):
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		case errors.Is(err, service.ErrInsufficientBalance):
			c.JSON(http.StatusPaymentRequired, gin.H{"error": err.Error()})
		default:
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		}
	}
}

// withdrawalView is the JSON general-backend gets back for a withdrawal: enough
// to show status and the on-chain signature once broadcast.
func withdrawalView(w *models.Withdrawal) gin.H {
	view := gin.H{
		"id":           w.ID,
		"status":       w.Status,
		"amount_reais": w.AmountReais,
		"amount_usdc":  w.AmountUSDC,
	}
	if w.Signature != nil {
		view["signature"] = *w.Signature
	}
	return view
}

func listDeposits(st *store.Store) gin.HandlerFunc {
	return func(c *gin.Context) {
		deps, err := st.ListDepositsByAccount(c.Request.Context(), c.Param("id"))
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"deposits": deps})
	}
}

func listWithdrawals(st *store.Store) gin.HandlerFunc {
	return func(c *gin.Context) {
		ws, err := st.ListWithdrawalsByAccount(c.Request.Context(), c.Param("id"))
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"withdrawals": ws})
	}
}
