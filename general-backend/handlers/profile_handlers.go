package handlers

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"tchebet/general-backend/auth"
	"tchebet/general-backend/betclient"
	"tchebet/general-backend/sanitize"
	"tchebet/general-backend/views"
	"tchebet/general-backend/walletclient"

	"github.com/gin-gonic/gin"
	"github.com/skip2/go-qrcode"
)

// Profile renders the account page. wc is non-nil only in crypto mode
// (DEPOSIT_MODE=crypto); when set, the deposit address/QR and crypto
// deposit/withdrawal history are fetched from wallet-backend and the R$
// fake-money top-up form is replaced by the crypto panels.
func Profile(bc *betclient.Client, wc *walletclient.Client) gin.HandlerFunc {
	return func(c *gin.Context) {
		acct := auth.CurrentAccount(c)
		user, err := bc.GetUser(c.Request.Context(), acct.ID)
		if err != nil {
			c.String(http.StatusBadGateway, "betting engine unavailable")
			return
		}

		bets, err := bc.ListUserBets(c.Request.Context(), acct.ID)
		if err != nil {
			c.String(http.StatusBadGateway, "betting engine unavailable")
			return
		}

		listings, err := bc.ListUserListings(c.Request.Context(), acct.ID)
		if err != nil {
			listings = nil // tolerate the listing engine being unreachable - bet history still renders
		}
		listingByBet := make(map[string]betclient.ListingDetails, len(listings))
		for _, l := range listings {
			listingByBet[l.BetID] = l
		}

		// Memoized by MarketID rather than called once per bet row - a user
		// with multiple bets on the same market previously re-fetched that
		// market's details redundantly.
		marketCache := map[string]*betclient.MarketDetails{}
		history := make([]views.BetHistoryItemVM, 0, len(bets))
		for _, b := range bets {
			question := b.MarketID
			status := ""
			details, ok := marketCache[b.MarketID]
			if !ok {
				details, _ = bc.GetMarketDetails(c.Request.Context(), b.MarketID)
				marketCache[b.MarketID] = details
			}
			if details != nil {
				question = details.Question
				status = details.Status
			}

			item := views.BetHistoryItemVM{
				BetID:          b.ID,
				MarketID:       b.MarketID,
				MarketQuestion: question,
				MarketStatus:   status,
				Outcome:        b.Outcome,
				StakeLabel:     views.FormatMoney(b.Stake),
				CreatedAtLabel: views.RelativeTimePT(b.CreatedAt),
			}
			if l, ok := listingByBet[b.ID]; ok {
				item.IsListed = true
				item.ListingID = l.ID
				item.AskingPriceLabel = views.FormatMoney(l.AskingPrice)
			}
			history = append(history, item)
		}

		vm := views.ProfileVM{
			Email:     acct.Email,
			BalanceRs: views.FormatMoney(user.Balance),
			Bets:      history,
		}
		if wc != nil {
			enrichCrypto(c.Request.Context(), wc, acct.ID, &vm)
		}

		nav := buildNav(c, bc)
		flash := c.Query("flash")
		views.Profile(nav, vm, flash).Render(c.Request.Context(), c.Writer)
	}
}

// enrichCrypto fills the crypto-mode fields from wallet-backend without doing
// on-chain work. Profile rendering must remain fast even when Solana Devnet is
// slow or unavailable; address provisioning is an explicit user action below.
func enrichCrypto(ctx context.Context, wc *walletclient.Client, accountID string, vm *views.ProfileVM) {
	vm.CryptoMode = true

	if addr, err := wc.LookupDepositAddress(ctx, accountID); err == nil {
		vm.DepositAddress = addr.Address
		vm.DepositQR = depositQR(addr.Address)
	} else if errors.Is(err, walletclient.ErrDepositAddressNotFound) {
		vm.NeedsDepositAddress = true
	} else {
		vm.DepositError = "Não foi possível consultar o endereço de depósito agora."
	}

	if deps, err := wc.ListDeposits(ctx, accountID); err == nil {
		for _, d := range deps {
			vm.Deposits = append(vm.Deposits, views.CryptoDepositVM{
				Asset:       d.Asset,
				Short:       shortHash(d.Signature),
				StatusLabel: depositStatusPT(d.Status),
				AmountLabel: depositAmountLabel(d),
			})
		}
	}

	if ws, err := wc.ListWithdrawals(ctx, accountID); err == nil {
		for _, w := range ws {
			vm.Withdrawals = append(vm.Withdrawals, views.CryptoWithdrawalVM{
				Dest:        shortHash(w.DestAddress),
				Short:       shortWithdrawalRef(w),
				StatusLabel: withdrawalStatusPT(w.Status),
				AmountLabel: views.FormatMoney(w.AmountReais),
			})
		}
	}
}

const depositAddressProvisionTimeout = 10 * time.Second

// ProvisionDepositAddress performs the slow on-chain setup only after an
// explicit user request. The short deadline prevents a flaky Devnet RPC from
// turning a normal browser navigation into a minute-long wait.
func ProvisionDepositAddress(wc *walletclient.Client) gin.HandlerFunc {
	return func(c *gin.Context) {
		acct := auth.CurrentAccount(c)
		ctx, cancel := context.WithTimeout(c.Request.Context(), depositAddressProvisionTimeout)
		defer cancel()

		if _, err := wc.ProvisionAccount(ctx, acct.ID); err != nil {
			c.Redirect(http.StatusFound, "/profile?flash="+url.QueryEscape("Não foi possível criar o endereço de depósito agora. Tente novamente em instantes."))
			return
		}
		c.Redirect(http.StatusFound, "/profile?flash="+url.QueryEscape("Endereço de depósito criado."))
	}
}

// depositQR renders a Solana address as a base64 PNG data URI (no external host,
// so it survives the strict no-CDN setup). Empty string on failure - the panel
// just omits the image.
func depositQR(address string) string {
	png, err := qrcode.Encode(address, qrcode.Medium, 256)
	if err != nil {
		return ""
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)
}

func depositAmountLabel(d walletclient.Deposit) string {
	if d.CreditedReais > 0 {
		return views.FormatMoney(d.CreditedReais)
	}
	return "—"
}

func shortHash(s string) string {
	if len(s) <= 12 {
		return s
	}
	return s[:6] + "…" + s[len(s)-4:]
}

func shortWithdrawalRef(w walletclient.Withdrawal) string {
	if w.Signature != nil {
		return shortHash(*w.Signature)
	}
	return shortHash(w.ID)
}

func depositStatusPT(s string) string {
	switch s {
	case "DETECTED":
		return "detectado"
	case "SWAPPING":
		return "convertendo"
	case "CREDITED":
		return "creditado"
	case "FAILED":
		return "falhou"
	default:
		return strings.ToLower(s)
	}
}

func withdrawalStatusPT(s string) string {
	switch s {
	case "REQUESTED":
		return "solicitado"
	case "SIGNING":
		return "assinando"
	case "SENT":
		return "enviado"
	case "CONFIRMED":
		return "confirmado"
	case "FAILED":
		return "falhou"
	default:
		return strings.ToLower(s)
	}
}

type depositRequest struct {
	Amount int64 `form:"amount" binding:"required"`
}

// Deposit is a real (if fake-money) top-up: reads the current balance via
// GetUser, adds the deposit, writes it back via SyncUser. Not exposed as a
// standalone JSON route on purpose - see the comment on the /users/sync
// wrapper in handlers.go.
func Deposit(bc *betclient.Client) gin.HandlerFunc {
	return func(c *gin.Context) {
		acct := auth.CurrentAccount(c)

		var req depositRequest
		if err := c.ShouldBind(&req); err != nil {
			c.Redirect(http.StatusFound, "/profile?flash=valor+inv%C3%A1lido")
			return
		}

		amount, err := sanitize.PositiveInt64(req.Amount, sanitize.MaxSanityInt)
		if err != nil {
			c.Redirect(http.StatusFound, "/profile?flash=valor+inv%C3%A1lido")
			return
		}

		user, err := bc.GetUser(c.Request.Context(), acct.ID)
		if err != nil {
			c.Redirect(http.StatusFound, "/profile?flash=motor+de+apostas+indispon%C3%ADvel")
			return
		}

		if _, err := bc.SyncUserTyped(c.Request.Context(), acct.ID, user.Balance+amount); err != nil {
			c.Redirect(http.StatusFound, "/profile?flash=falha+ao+depositar")
			return
		}

		c.Redirect(http.StatusFound, "/profile?flash=Dep%C3%B3sito+realizado")
	}
}

type withdrawFormRequest struct {
	DestAddress string `form:"dest_address" binding:"required"`
	Amount      int64  `form:"amount" binding:"required"`
}

// Withdraw (crypto mode only) validates a destination + R$ amount and asks
// wallet-backend to debit the ledger and pay out USDC. The amount is whole BRL
// (the ledger unit); wallet-backend converts to micro-USDC at the fixed rate.
func Withdraw(bc *betclient.Client, wc *walletclient.Client) gin.HandlerFunc {
	return func(c *gin.Context) {
		acct := auth.CurrentAccount(c)

		var req withdrawFormRequest
		if err := c.ShouldBind(&req); err != nil {
			c.Redirect(http.StatusFound, "/profile?flash="+url.QueryEscape("dados de saque inválidos"))
			return
		}
		dest, err := sanitize.SolanaAddress(req.DestAddress)
		if err != nil {
			c.Redirect(http.StatusFound, "/profile?flash="+url.QueryEscape("endereço de destino inválido"))
			return
		}
		amount, err := sanitize.PositiveInt64(req.Amount, sanitize.MaxSanityInt)
		if err != nil {
			c.Redirect(http.StatusFound, "/profile?flash="+url.QueryEscape("valor inválido"))
			return
		}

		_, err = wc.RequestWithdrawal(c.Request.Context(), acct.ID, dest, amount)
		switch {
		case err == nil:
			c.Redirect(http.StatusFound, "/profile?flash="+url.QueryEscape("Saque enviado"))
		case errors.Is(err, walletclient.ErrManualReview):
			c.Redirect(http.StatusFound, "/profile?flash="+url.QueryEscape("Saque em análise manual"))
		case errors.Is(err, walletclient.ErrInsufficientBalance):
			c.Redirect(http.StatusFound, "/profile?flash="+url.QueryEscape("saldo insuficiente"))
		default:
			c.Redirect(http.StatusFound, "/profile?flash="+url.QueryEscape("falha ao sacar"))
		}
	}
}

// Faucet (crypto mode only, demo affordance) asks wallet-backend to send the
// logged-in account its one-time devnet USDC grant from treasury. The grant is
// credited by wallet-backend's deposit poller, so the balance reflects on the
// next crypto-status poll - here we just flash the outcome and redirect.
func Faucet(wc *walletclient.Client) gin.HandlerFunc {
	return func(c *gin.Context) {
		acct := auth.CurrentAccount(c)
		_, err := wc.Faucet(c.Request.Context(), acct.ID)
		switch {
		case err == nil:
			c.Redirect(http.StatusFound, "/profile?flash="+url.QueryEscape("USDC de teste enviado — o saldo será creditado em instantes"))
		case errors.Is(err, walletclient.ErrFaucetAlreadyClaimed):
			c.Redirect(http.StatusFound, "/profile?flash="+url.QueryEscape("Você já resgatou o USDC de teste"))
		default:
			c.Redirect(http.StatusFound, "/profile?flash="+url.QueryEscape("falha ao resgatar USDC de teste"))
		}
	}
}

// CryptoStatus renders just the deposit/balance panel - the htmx polling target
// on the crypto profile page, so a confirmed deposit reflects without a reload.
func CryptoStatus(bc *betclient.Client, wc *walletclient.Client) gin.HandlerFunc {
	return func(c *gin.Context) {
		acct := auth.CurrentAccount(c)
		user, err := bc.GetUser(c.Request.Context(), acct.ID)
		if err != nil {
			c.String(http.StatusBadGateway, "betting engine unavailable")
			return
		}
		vm := views.ProfileVM{
			Email:     acct.Email,
			BalanceRs: views.FormatMoney(user.Balance),
		}
		enrichCrypto(c.Request.Context(), wc, acct.ID, &vm)
		views.CryptoStatusPanel(vm).Render(c.Request.Context(), c.Writer)
	}
}
