// viewmodels.go holds the plain Go structs templ files render - handlers
// build these from betclient data so the views package stays decoupled from
// betclient/store entirely (easier to reason about, easier to test the
// formatting helpers below in isolation).
package views

import (
	"fmt"
	"strings"
	"time"
)

type NavVM struct {
	LoggedIn  bool
	IsAdmin   bool
	BalanceRs string
}

type MarketCardVM struct {
	ID           string
	Question     string
	SimPct       int
	NaoPct       int
	VolumeLabel  string
	BettorCount  int
	IsLive       bool
	LowLiquidity bool
}

type ActivityItemVM struct {
	MaskedLabel  string
	Outcome      string
	IsSim        bool
	StakeLabel   string
	RelativeTime string
}

type SparklinePoint struct {
	X, Y float64
}

type MarketDetailVM struct {
	ID             string
	Question       string
	Status         string
	IsLive         bool
	CreatedAtLabel string
	Bettors        int
	VolumeLabel    string
	SimPct         int
	NaoPct         int
	SimPriceLabel  string
	NaoPriceLabel  string
	LowLiquidity   bool
	SparklinePoly  string // pre-built SVG "x,y x,y ..." points attribute
	Activity       []ActivityItemVM
	CanBet         bool // status == OPEN
	ForSale        []ListingItemVM
}

type BetHistoryItemVM struct {
	BetID            string
	MarketID         string
	MarketQuestion   string
	MarketStatus     string
	Outcome          string
	StakeLabel       string
	CreatedAtLabel   string
	IsListed         bool
	ListingID        string
	AskingPriceLabel string
}

type ListingItemVM struct {
	ListingID         string
	BetID             string
	Outcome           string
	IsSim             bool
	StakeLabel        string
	AskingPriceLabel  string
	SellerMaskedLabel string
	IsMine            bool
	CreatedAtLabel    string
}

type ProfileVM struct {
	Email     string
	BalanceRs string
	Bets      []BetHistoryItemVM

	// Crypto deposit mode (DEPOSIT_MODE=crypto). All zero/empty in fake mode,
	// where the profile renders the R$ fake-money top-up form instead.
	CryptoMode          bool
	DepositAddress      string
	DepositQR           string // data URI PNG of the deposit address
	NeedsDepositAddress bool   // true until the user explicitly creates their deposit target
	DepositError        string // set when the address couldn't be provisioned (shown instead of a blank field)
	Deposits            []CryptoDepositVM
	Withdrawals         []CryptoWithdrawalVM
}

// CryptoDepositVM / CryptoWithdrawalVM are the per-row status lines shown under
// the crypto deposit/withdraw panels (the balance itself comes from bet-backend).
type CryptoDepositVM struct {
	Asset       string
	Short       string // shortened signature
	StatusLabel string
	AmountLabel string
}

type CryptoWithdrawalVM struct {
	Dest        string
	Short       string
	StatusLabel string
	AmountLabel string
}

type AdminMarketRowVM struct {
	ID          string
	Question    string
	Status      string
	VolumeLabel string
}

type AdminUserRowVM struct {
	ID             string
	Email          string
	IsAdmin        bool
	CreatedAtLabel string
}

// FormatMoney renders a raw int64 balance/stake as "R$ 1.234,00" - the
// ledger has no concept of cents (bet-backend just moves whole int64
// units), so this always shows ",00".
func FormatMoney(v int64) string {
	neg := v < 0
	if neg {
		v = -v
	}
	s := fmt.Sprintf("%d", v)
	var out []byte
	for i, c := range []byte(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, '.')
		}
		out = append(out, c)
	}
	sign := ""
	if neg {
		sign = "-"
	}
	return sign + "R$ " + string(out) + ",00"
}

// MaskEmail turns "maria@example.com" into "m***ia" for the activity feed -
// enough to feel personal without showing a real email in a public page.
func MaskEmail(email string) string {
	local := email
	if i := strings.IndexByte(email, '@'); i >= 0 {
		local = email[:i]
	}
	if len(local) <= 3 {
		return string(local[0]) + "***"
	}
	return string(local[0]) + "***" + local[len(local)-2:]
}

// RelativeTimePT formats a timestamp as "agora" / "há Xmin" / "há Xh" / "há Xd".
func RelativeTimePT(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "agora"
	case d < time.Hour:
		return fmt.Sprintf("há %d min", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("há %dh", int(d.Hours()))
	default:
		return fmt.Sprintf("há %dd", int(d.Hours()/24))
	}
}
