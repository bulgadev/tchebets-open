package handlers

import (
	"net/http"

	"tchebet/general-backend/auth"
	"tchebet/general-backend/betclient"
	"tchebet/general-backend/sanitize"
	"tchebet/general-backend/store"
	"tchebet/general-backend/views"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type registerRequest struct {
	Email    string `form:"email" binding:"required,email"`
	Password string `form:"password" binding:"required"`
}

type loginRequest struct {
	Email    string `form:"email" binding:"required,email"`
	Password string `form:"password" binding:"required"`
}

func RegisterPage(c *gin.Context) {
	views.AuthCard("Criar Conta", "", "/register", "CRIAR CONTA", "Já tem conta?", "/login", "Entrar").Render(c.Request.Context(), c.Writer)
}

func registerErr(c *gin.Context, msg string) {
	c.Writer.WriteHeader(http.StatusBadRequest)
	views.AuthCard("Criar Conta", msg, "/register", "CRIAR CONTA", "Já tem conta?", "/login", "Entrar").Render(c.Request.Context(), c.Writer)
}

func Register(bc *betclient.Client) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req registerRequest
		if err := c.ShouldBind(&req); err != nil {
			registerErr(c, "email/senha inválidos")
			return
		}

		email, err := sanitize.NonEmptyString(req.Email, maxEmailLen)
		if err != nil {
			registerErr(c, err.Error())
			return
		}
		if req.Password == "" {
			registerErr(c, "senha é obrigatória")
			return
		}

		passwordHash, err := auth.HashPassword(req.Password)
		if err != nil {
			registerErr(c, "falha ao processar senha")
			return
		}

		id := uuid.New().String()
		if _, err := store.CreateAccount(id, email, passwordHash); err != nil {
			if err == store.ErrEmailTaken {
				registerErr(c, "email já cadastrado")
				return
			}
			registerErr(c, "falha ao criar conta")
			return
		}

		// Provision the matching bet-backend user at balance 0. Not via the
		// public /users/sync route - a browser-reachable "set anyone's balance
		// by UUID" endpoint would be a real hole now that accounts exist.
		if _, err := bc.SyncUserTyped(c.Request.Context(), id, 0); err != nil {
			registerErr(c, "motor de apostas indisponível, tente novamente")
			return
		}

		auth.Login(c, id)
		c.Redirect(http.StatusFound, "/")
	}
}

func LoginPage(c *gin.Context) {
	views.AuthCard("Entrar", "", "/login", "ENTRAR", "Não tem conta?", "/register", "Criar conta").Render(c.Request.Context(), c.Writer)
}

func Login(c *gin.Context) {
	var req loginRequest
	if err := c.ShouldBind(&req); err != nil {
		c.Writer.WriteHeader(http.StatusBadRequest)
		views.AuthCard("Entrar", "email/senha inválidos", "/login", "ENTRAR", "Não tem conta?", "/register", "Criar conta").Render(c.Request.Context(), c.Writer)
		return
	}

	acct, err := store.FindAccountByEmail(req.Email)
	if err != nil || !auth.CheckPassword(acct.PasswordHash, req.Password) {
		c.Writer.WriteHeader(http.StatusUnauthorized)
		views.AuthCard("Entrar", "email ou senha incorretos", "/login", "ENTRAR", "Não tem conta?", "/register", "Criar conta").Render(c.Request.Context(), c.Writer)
		return
	}

	auth.Login(c, acct.ID)

	next := c.Query("next")
	if next == "" {
		next = "/"
	}
	c.Redirect(http.StatusFound, next)
}

func Logout(c *gin.Context) {
	auth.Logout(c)
	c.Redirect(http.StatusFound, "/")
}