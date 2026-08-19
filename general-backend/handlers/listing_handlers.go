package handlers

import (
	"errors"
	"net/http"

	"tchebet/general-backend/auth"
	"tchebet/general-backend/betclient"
	"tchebet/general-backend/sanitize"

	"github.com/gin-gonic/gin"
)

type listBetForSaleFormRequest struct {
	AskingPrice int64 `form:"asking_price" binding:"required"`
}

// ListBetForSalePage is the Htmx-free, session-authenticated form post from
// the profile page's bet history - mirrors Deposit's redirect-with-flash
// pattern rather than PlaceBetPage's Htmx fragment one, since it lives on
// the same page as Deposit.
func ListBetForSalePage(bc *betclient.Client) gin.HandlerFunc {
	return func(c *gin.Context) {
		betID, err := sanitize.UUID(c.Param("id"))
		if err != nil {
			c.Redirect(http.StatusFound, "/profile?flash=aposta+inv%C3%A1lida")
			return
		}

		var req listBetForSaleFormRequest
		if err := c.ShouldBind(&req); err != nil {
			c.Redirect(http.StatusFound, "/profile?flash=pre%C3%A7o+inv%C3%A1lido")
			return
		}
		askingPrice, err := sanitize.PositiveInt64(req.AskingPrice, sanitize.MaxSanityInt)
		if err != nil {
			c.Redirect(http.StatusFound, "/profile?flash=pre%C3%A7o+inv%C3%A1lido")
			return
		}

		sellerID, ok := auth.CurrentUserID(c)
		if !ok {
			c.Redirect(http.StatusFound, "/login")
			return
		}

		if _, err := bc.ListBetForSaleTyped(c.Request.Context(), betID, sellerID, askingPrice); err != nil {
			msg := "n%C3%A3o+foi+poss%C3%ADvel+colocar+a+aposta+%C3%A0+venda"
			var se *betclient.StatusError
			if errors.As(err, &se) {
				msg = se.Message
			}
			c.Redirect(http.StatusFound, "/profile?flash="+msg)
			return
		}

		c.Redirect(http.StatusFound, "/profile?flash=Aposta+colocada+%C3%A0+venda")
	}
}

// CancelListingPage withdraws one of the current user's own active listings.
func CancelListingPage(bc *betclient.Client) gin.HandlerFunc {
	return func(c *gin.Context) {
		listingID, err := sanitize.UUID(c.Param("id"))
		if err != nil {
			c.Redirect(http.StatusFound, "/profile?flash=listagem+inv%C3%A1lida")
			return
		}

		sellerID, ok := auth.CurrentUserID(c)
		if !ok {
			c.Redirect(http.StatusFound, "/login")
			return
		}

		if err := bc.CancelListingTyped(c.Request.Context(), listingID, sellerID); err != nil {
			c.Redirect(http.StatusFound, "/profile?flash=falha+ao+cancelar+venda")
			return
		}

		c.Redirect(http.StatusFound, "/profile?flash=Venda+cancelada")
	}
}

// BuyListingPage handles the market page's "Comprar" button - an Htmx
// fragment response, mirroring PlaceBetPage's own green/red result pattern.
func BuyListingPage(bc *betclient.Client) gin.HandlerFunc {
	return func(c *gin.Context) {
		listingID, err := sanitize.UUID(c.Param("id"))
		if err != nil {
			c.String(http.StatusBadRequest, `<div class="text-tche-red text-sm">listagem inválida</div>`)
			return
		}

		buyerID, ok := auth.CurrentUserID(c)
		if !ok {
			c.String(http.StatusUnauthorized, `<div class="text-tche-red text-sm">faça login para comprar</div>`)
			return
		}

		listing, err := bc.BuyListingTyped(c.Request.Context(), listingID, buyerID)
		if err != nil {
			msg := "não foi possível concluir a compra"
			var se *betclient.StatusError
			if errors.As(err, &se) {
				msg = se.Message
			}
			c.String(http.StatusOK, `<div class="text-tche-red text-sm">`+msg+`</div>`)
			return
		}

		c.String(http.StatusOK, `<div class="text-tche-green text-sm">Compra realizada! <a href="/markets/`+listing.MarketID+`" class="underline">Atualizar página</a></div>`)
	}
}
