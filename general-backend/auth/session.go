// Package auth is general-backend's session/identity layer - cookie-only,
// no server-side session table, per the explicit decision to keep this
// service as infra-light as possible. The cookie just holds a user id;
// everything else (email, is_admin) is loaded fresh from the accounts table
// on every request that needs it.
package auth

import (
	"net/http"
	"net/url"

	"tchebet/general-backend/store"

	"github.com/gin-contrib/sessions"
	"github.com/gin-gonic/gin"
)

const sessionUserKey = "user_id"
const contextAccountKey = "account"

// Login stashes the user id in the session cookie.
func Login(c *gin.Context, userID string) {
	session := sessions.Default(c)
	session.Set(sessionUserKey, userID)
	session.Save()
}

// Logout clears the session cookie.
func Logout(c *gin.Context) {
	session := sessions.Default(c)
	session.Clear()
	session.Save()
}

// CurrentUserID reads the user id straight out of the session, no DB hit.
func CurrentUserID(c *gin.Context) (string, bool) {
	session := sessions.Default(c)
	v := session.Get(sessionUserKey)
	id, ok := v.(string)
	if !ok || id == "" {
		return "", false
	}
	return id, true
}

// CurrentAccount reads the *store.Account stashed on the context by
// RequireUser - only valid inside handlers behind that middleware.
func CurrentAccount(c *gin.Context) *store.Account {
	v, ok := c.Get(contextAccountKey)
	if !ok {
		return nil
	}
	acct, _ := v.(*store.Account)
	return acct
}

// RequireUser redirects to /login (preserving the original path as ?next=)
// if there's no valid session, otherwise loads the account and makes it
// available to the handler/templates via CurrentAccount.
func RequireUser() gin.HandlerFunc {
	return func(c *gin.Context) {
		userID, ok := CurrentUserID(c)
		if !ok {
			redirectToLogin(c)
			return
		}

		acct, err := store.FindAccountByID(userID)
		if err != nil {
			// Stale/invalid session (account deleted, etc) - clear it and bounce to login.
			Logout(c)
			redirectToLogin(c)
			return
		}

		c.Set(contextAccountKey, acct)
		c.Next()
	}
}

// RequireAdmin must be chained after RequireUser - it 403s non-admins
// instead of leaking whether the route exists.
func RequireAdmin() gin.HandlerFunc {
	return func(c *gin.Context) {
		acct := CurrentAccount(c)
		if acct == nil || !acct.IsAdmin {
			c.String(http.StatusForbidden, "admins only")
			c.Abort()
			return
		}
		c.Next()
	}
}

func redirectToLogin(c *gin.Context) {
	next := url.QueryEscape(c.Request.URL.RequestURI())
	c.Redirect(http.StatusFound, "/login?next="+next)
	c.Abort()
}
