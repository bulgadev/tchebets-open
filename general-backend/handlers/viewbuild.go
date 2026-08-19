// viewbuild.go turns betclient/store data into views' plain VM structs -
// kept separate from the route handlers themselves so the "how do we derive
// AO VIVO / low liquidity / activity feed / sparkline from what
// bet-backend actually returns" logic lives in one readable place.
package handlers

import (
	"fmt"
	"sort"

	"tchebet/general-backend/betclient"
	"tchebet/general-backend/store"
	"tchebet/general-backend/views"
)

const lowLiquidityThreshold = 500

// hasRealBets tells seed-only markets apart from ones with actual betting
// activity without an extra call per market: seeding puts exactly
// seed*len(outcomes) into the total pool, so anything above that means a
// real bet landed.
func hasRealBets(m betclient.MarketDetails) bool {
	seedTotal := m.Seed * int64(len(m.Outcomes))
	return m.TotalPool > seedTotal
}

func pct(part, total int64) int {
	if total <= 0 {
		return 0
	}
	return int(part * 100 / total)
}

func bettorCount(bets []betclient.Bet) int {
	seen := map[string]bool{}
	for _, b := range bets {
		if b.UserID == houseUserID {
			continue
		}
		seen[b.UserID] = true
	}
	return len(seen)
}

const houseUserID = "00000000-0000-0000-0000-000000000000"

func buildMarketCardVM(m betclient.MarketDetails) views.MarketCardVM {
	simPct := pct(m.Pools["SIM"], m.TotalPool)
	return views.MarketCardVM{
		ID:           m.ID,
		Question:     m.Question,
		SimPct:       simPct,
		NaoPct:       100 - simPct,
		VolumeLabel:  views.FormatMoney(m.TotalPool),
		BettorCount:  0, // avoided an extra ListMarketBets call per home-grid card; shown on the detail page instead
		IsLive:       m.Status == "OPEN" && hasRealBets(m),
		LowLiquidity: m.TotalPool < lowLiquidityThreshold,
	}
}

// buildSparklinePoly replays a market's ordered bet list into an SVG
// polyline "x,y x,y ..." points string tracking SIM-% over time - the one
// dataset (ListMarketBets) that powers both this and the activity feed
// below, so there's no separate odds-snapshot table anywhere.
func buildSparklinePoly(m betclient.MarketDetails, bets []betclient.Bet) string {
	simTotal := int64(0)
	naoTotal := int64(0)
	type point struct{ pct float64 }
	var points []point

	for _, b := range bets {
		if b.Outcome == "SIM" {
			simTotal += b.Stake
		} else {
			naoTotal += b.Stake
		}
		total := simTotal + naoTotal
		p := 50.0
		if total > 0 {
			p = float64(simTotal) / float64(total) * 100
		}
		points = append(points, point{pct: p})
	}

	if len(points) == 0 {
		// Flat line at the current split - no bet history yet to plot.
		currentPct := float64(pct(m.Pools["SIM"], m.TotalPool))
		return fmt.Sprintf("0,%.1f 500,%.1f", 70-currentPct*0.7, 70-currentPct*0.7)
	}

	step := 500.0
	if len(points) > 1 {
		step = 500.0 / float64(len(points)-1)
	}
	poly := ""
	for i, p := range points {
		x := float64(i) * step
		y := 70 - (p.pct * 0.7) // 0-100% mapped to svg y=70..0
		if i > 0 {
			poly += " "
		}
		poly += fmt.Sprintf("%.1f,%.1f", x, y)
	}
	return poly
}

func buildActivityFeed(bets []betclient.Bet, accountsByID map[string]*store.Account, limit int) []views.ActivityItemVM {
	// Newest first, house seed bets excluded - they aren't "someone betting".
	var real []betclient.Bet
	for _, b := range bets {
		if b.UserID != houseUserID {
			real = append(real, b)
		}
	}
	sort.Slice(real, func(i, j int) bool { return real[i].CreatedAt.After(real[j].CreatedAt) })
	if len(real) > limit {
		real = real[:limit]
	}

	items := make([]views.ActivityItemVM, 0, len(real))
	for _, b := range real {
		label := "alguém"
		if acct, ok := accountsByID[b.UserID]; ok {
			label = views.MaskEmail(acct.Email)
		}
		items = append(items, views.ActivityItemVM{
			MaskedLabel:  label,
			Outcome:      b.Outcome,
			IsSim:        b.Outcome == "SIM",
			StakeLabel:   views.FormatMoney(b.Stake),
			RelativeTime: views.RelativeTimePT(b.CreatedAt),
		})
	}
	return items
}

func buildMarketDetailVM(m betclient.MarketDetails, bets []betclient.Bet, accountsByID map[string]*store.Account, listings []betclient.ListingDetails, viewerID string) views.MarketDetailVM {
	simPct := pct(m.Pools["SIM"], m.TotalPool)
	naoPct := 100 - simPct

	return views.MarketDetailVM{
		ID:             m.ID,
		Question:       m.Question,
		Status:         m.Status,
		IsLive:         m.Status == "OPEN" && hasRealBets(m),
		CreatedAtLabel: m.CreatedAt.Format("02 Jan · 15:04"),
		Bettors:        bettorCount(bets),
		VolumeLabel:    views.FormatMoney(m.TotalPool),
		SimPct:         simPct,
		NaoPct:         naoPct,
		SimPriceLabel:  centsPriceLabel(simPct),
		NaoPriceLabel:  centsPriceLabel(naoPct),
		LowLiquidity:   m.TotalPool < lowLiquidityThreshold,
		SparklinePoly:  buildSparklinePoly(m, bets),
		Activity:       buildActivityFeed(bets, accountsByID, 10),
		CanBet:         m.Status == "OPEN",
		ForSale:        buildForSaleVM(listings, accountsByID, viewerID),
	}
}

// mergeAccountsForListings resolves any listing sellers not already present
// in accountsByID (built from the market's bets) - a seller who has since
// sold their only bet on this market wouldn't otherwise appear as a current
// bettor.
func mergeAccountsForListings(listings []betclient.ListingDetails, accountsByID map[string]*store.Account) {
	for _, l := range listings {
		if _, ok := accountsByID[l.SellerID]; ok || l.SellerID == houseUserID {
			continue
		}
		if acct, err := store.FindAccountByID(l.SellerID); err == nil {
			accountsByID[l.SellerID] = acct
		}
	}
}

func buildForSaleVM(listings []betclient.ListingDetails, accountsByID map[string]*store.Account, viewerID string) []views.ListingItemVM {
	items := make([]views.ListingItemVM, 0, len(listings))
	for _, l := range listings {
		label := "alguém"
		if acct, ok := accountsByID[l.SellerID]; ok {
			label = views.MaskEmail(acct.Email)
		}
		items = append(items, views.ListingItemVM{
			ListingID:         l.ID,
			BetID:             l.BetID,
			Outcome:           l.Outcome,
			IsSim:             l.Outcome == "SIM",
			StakeLabel:        views.FormatMoney(l.Stake),
			AskingPriceLabel:  views.FormatMoney(l.AskingPrice),
			SellerMaskedLabel: label,
			IsMine:            viewerID != "" && l.SellerID == viewerID,
			CreatedAtLabel:    views.RelativeTimePT(l.CreatedAt),
		})
	}
	return items
}

func centsPriceLabel(p int) string {
	return fmt.Sprintf("R$ 0,%02d", p)
}
