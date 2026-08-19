package handlers

import (
	"tchebet/general-backend/auth"
	"tchebet/general-backend/betclient"
	"tchebet/general-backend/store"
	"tchebet/general-backend/views"

	"github.com/gin-gonic/gin"
)

// buildNav reads the current session (if any) to fill the nav bar - balance
// comes straight from bet-backend, never cached, so it's always right after
// a bet/deposit. Looks up the account directly instead of relying on
// auth.CurrentAccount, which is only populated by RequireUser() - callers
// of buildNav (Home, market detail page) are public routes that middleware
// never runs on.
func buildNav(c *gin.Context, bc *betclient.Client) views.NavVM {
	userID, ok := auth.CurrentUserID(c)
	if !ok {
		return views.NavVM{}
	}

	acct, err := store.FindAccountByID(userID)
	if err != nil {
		return views.NavVM{}
	}

	balanceLabel := views.FormatMoney(0)
	if user, err := bc.GetUser(c.Request.Context(), userID); err == nil {
		balanceLabel = views.FormatMoney(user.Balance)
	}

	return views.NavVM{LoggedIn: true, IsAdmin: acct.IsAdmin, BalanceRs: balanceLabel}
}
