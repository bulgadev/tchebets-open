package handlers

import (
	"errors"
	"net/http"

	"tchebet/general-backend/betclient"
	"tchebet/general-backend/sanitize"
	"tchebet/general-backend/store"
	"tchebet/general-backend/views"

	"github.com/gin-gonic/gin"
)

// binaryOutcomes is hardcoded (not user-entered) per the Phase 2 decision to
// keep the frontend strictly SIM/NÃO markets, matching the design reference
// - bet-backend's engine stays N-outcome generic underneath.
var binaryOutcomes = []string{"SIM", "NÃO"}

func AdminMarkets(bc *betclient.Client) gin.HandlerFunc {
	return func(c *gin.Context) {
		markets, err := bc.ListMarkets(c.Request.Context())
		if err != nil {
			c.String(http.StatusBadGateway, "betting engine unavailable")
			return
		}

		rows := make([]views.AdminMarketRowVM, 0, len(markets))
		for _, m := range markets {
			rows = append(rows, views.AdminMarketRowVM{
				ID:          m.ID,
				Question:    m.Question,
				Status:      m.Status,
				VolumeLabel: views.FormatMoney(m.TotalPool),
			})
		}

		nav := buildNav(c, bc)
		views.AdminMarketsPage(nav, rows, c.Query("error")).Render(c.Request.Context(), c.Writer)
	}
}

func AdminNewMarketPageHandler(bc *betclient.Client) gin.HandlerFunc {
	return func(c *gin.Context) {
		nav := buildNav(c, bc)
		views.AdminNewMarketPage(nav, "", Teams).Render(c.Request.Context(), c.Writer)
	}
}

type createMarketFormRequest struct {
	Question string `form:"question"`
	TeamA    string `form:"team_a"`
	TeamB    string `form:"team_b"`
	Seed     int64  `form:"seed"`
}

// buildQuestionFromTeams validates both team names against the hardcoded
// Teams list and builds the canonical question server-side - never trusts a
// client-constructed question string, since a submitted team_a/team_b could
// otherwise bypass the <select> and inject arbitrary text.
func buildQuestionFromTeams(rawTeamA, rawTeamB string) (string, error) {
	teamA, err := sanitize.NonEmptyString(rawTeamA, maxTeamLen)
	if err != nil {
		return "", errors.New("time A inválido")
	}
	teamB, err := sanitize.NonEmptyString(rawTeamB, maxTeamLen)
	if err != nil {
		return "", errors.New("time B inválido")
	}
	if !isKnownTeam(teamA) || !isKnownTeam(teamB) {
		return "", errors.New("time desconhecido")
	}
	if teamA == teamB {
		return "", errors.New("os times devem ser diferentes")
	}
	return teamA + " vencerá o " + teamB + "?", nil
}

func AdminCreateMarket(bc *betclient.Client) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req createMarketFormRequest
		if err := c.ShouldBind(&req); err != nil {
			nav := buildNav(c, bc)
			views.AdminNewMarketPage(nav, "dados inválidos", Teams).Render(c.Request.Context(), c.Writer)
			return
		}

		var question string
		if req.TeamA != "" || req.TeamB != "" {
			q, err := buildQuestionFromTeams(req.TeamA, req.TeamB)
			if err != nil {
				nav := buildNav(c, bc)
				views.AdminNewMarketPage(nav, err.Error(), Teams).Render(c.Request.Context(), c.Writer)
				return
			}
			question = q
		} else {
			q, err := sanitize.NonEmptyString(req.Question, maxQuestionLen)
			if err != nil {
				nav := buildNav(c, bc)
				views.AdminNewMarketPage(nav, err.Error(), Teams).Render(c.Request.Context(), c.Writer)
				return
			}
			question = q
		}

		seed, err := sanitize.NonNegativeInt64(req.Seed, sanitize.MaxSanityInt)
		if err != nil {
			nav := buildNav(c, bc)
			views.AdminNewMarketPage(nav, err.Error(), Teams).Render(c.Request.Context(), c.Writer)
			return
		}

		market, err := bc.CreateMarketTyped(c.Request.Context(), question, binaryOutcomes, seed)
		if err != nil {
			nav := buildNav(c, bc)
			// Surface the real bet-backend error (e.g. "house has insufficient
			// balance to seed market") instead of a generic message - makes
			// admin debugging possible without digging through server logs.
			msg := "falha ao criar mercado"
			var se *betclient.StatusError
			if errors.As(err, &se) {
				msg = "falha ao criar mercado: " + se.Message
			}
			views.AdminNewMarketPage(nav, msg, Teams).Render(c.Request.Context(), c.Writer)
			return
		}

		c.Redirect(http.StatusFound, "/markets/"+market.ID)
	}
}

func AdminLockMarket(bc *betclient.Client) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, err := sanitize.UUID(c.Param("id"))
		if err != nil {
			c.Redirect(http.StatusFound, "/admin/markets?error=id+inv%C3%A1lido")
			return
		}
		if _, err := bc.LockMarket(c.Request.Context(), id); err != nil {
			c.Redirect(http.StatusFound, "/admin/markets?error=falha+ao+travar")
			return
		}
		c.Redirect(http.StatusFound, "/admin/markets")
	}
}

type resolveMarketFormRequest struct {
	WinningOutcome string `form:"winning_outcome" binding:"required"`
}

func AdminResolveMarket(bc *betclient.Client) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, err := sanitize.UUID(c.Param("id"))
		if err != nil {
			c.Redirect(http.StatusFound, "/admin/markets?error=id+inv%C3%A1lido")
			return
		}

		var req resolveMarketFormRequest
		if err := c.ShouldBind(&req); err != nil {
			c.Redirect(http.StatusFound, "/admin/markets?error=resultado+inv%C3%A1lido")
			return
		}
		outcome, err := sanitize.NonEmptyString(req.WinningOutcome, maxOutcomeLen)
		if err != nil {
			c.Redirect(http.StatusFound, "/admin/markets?error=resultado+inv%C3%A1lido")
			return
		}

		if _, err := bc.ResolveMarket(c.Request.Context(), id, outcome); err != nil {
			c.Redirect(http.StatusFound, "/admin/markets?error=falha+ao+resolver")
			return
		}
		c.Redirect(http.StatusFound, "/admin/markets")
	}
}

func AdminCancelMarket(bc *betclient.Client) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, err := sanitize.UUID(c.Param("id"))
		if err != nil {
			c.Redirect(http.StatusFound, "/admin/markets?error=id+inv%C3%A1lido")
			return
		}
		if _, err := bc.CancelMarket(c.Request.Context(), id); err != nil {
			c.Redirect(http.StatusFound, "/admin/markets?error=falha+ao+cancelar")
			return
		}
		c.Redirect(http.StatusFound, "/admin/markets")
	}
}

// AdminUsers lists every account so an existing admin can promote others -
// the only path to admin status once the initial bootstrap account (see
// store.CreateAccount) already exists.
func AdminUsers(bc *betclient.Client) gin.HandlerFunc {
	return func(c *gin.Context) {
		accounts, err := store.ListAccounts()
		if err != nil {
			c.String(http.StatusInternalServerError, "falha ao listar usuários")
			return
		}

		rows := make([]views.AdminUserRowVM, 0, len(accounts))
		for _, a := range accounts {
			rows = append(rows, views.AdminUserRowVM{
				ID:             a.ID,
				Email:          a.Email,
				IsAdmin:        a.IsAdmin,
				CreatedAtLabel: views.RelativeTimePT(a.CreatedAt),
			})
		}

		nav := buildNav(c, bc)
		views.AdminUsersPage(nav, rows, c.Query("error")).Render(c.Request.Context(), c.Writer)
	}
}

type promoteUserFormRequest struct {
	Email string `form:"email" binding:"required,email"`
}

func AdminPromoteUser(bc *betclient.Client) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req promoteUserFormRequest
		if err := c.ShouldBind(&req); err != nil {
			c.Redirect(http.StatusFound, "/admin/users?error=email+inv%C3%A1lido")
			return
		}

		if err := store.PromoteToAdmin(req.Email); err != nil {
			c.Redirect(http.StatusFound, "/admin/users?error=falha+ao+promover")
			return
		}

		c.Redirect(http.StatusFound, "/admin/users")
	}
}
