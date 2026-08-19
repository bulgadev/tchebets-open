package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"tchebet/bet-backend/cache"
	"tchebet/bet-backend/db"
	"tchebet/bet-backend/models"

	"github.com/google/uuid"
)

const HouseUserID = "00000000-0000-0000-0000-000000000000"

// If this errors scale, use a struct (or const, my brain is failing syntax, srry) with an switch case
var ErrMarketNotOpen = errors.New("market is not open for betting")
var ErrMarketNotLocked = errors.New("market must be locked before resolution")
var ErrInsufficientBalance = errors.New("insufficient user balance")
var ErrInvalidOutcome = errors.New("invalid outcome for this market")
var ErrMarketAlreadyResolved = errors.New("market already resolved")
var ErrMarketAlreadyCancelled = errors.New("market already cancelled")
var ErrInvalidStateTransition = errors.New("invalid market status transition")

// Good constructors in this file. // Feel free to transform somethings in nested functions if they scale too much.

// CreateMarket creates a new market, initializes its outcome pools, and injects seeds
func CreateMarket(question string, outcomes []string, seed int64) (*models.Market, error) {
	if len(outcomes) < 2 {
		return nil, errors.New("a market must have at least 2 outcomes")
	}
	if seed < 0 {
		return nil, errors.New("seed must be non-negative")
	}

	tx, err := db.DB.BeginTx(context.Background(), nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	marketID := uuid.New().String()

	outcomesJSON, err := json.Marshal(outcomes)
	if err != nil {
		return nil, err
	}

	// 1. Insert Market
	queryMarket := `
		INSERT INTO markets (id, question, outcomes, status, seed, winning_outcome, created_at)
		VALUES ($1, $2, $3, 'OPEN', $4, NULL, NOW())
	`
	_, err = tx.Exec(queryMarket, marketID, question, outcomesJSON, seed)
	if err != nil {
		return nil, err
	}

	// 2. Debit the house user balance for the total seed injected
	totalSeedRequired := seed * int64(len(outcomes))
	if totalSeedRequired > 0 {
		// Lock the house user to verify balance and debit
		var houseBalance int64
		err = tx.QueryRow(`SELECT balance FROM users WHERE id = $1 FOR UPDATE`, HouseUserID).Scan(&houseBalance)
		if err != nil {
			return nil, fmt.Errorf("failed to lock house user: %w", err)
		}
		if houseBalance < totalSeedRequired {
			return nil, fmt.Errorf("house has insufficient balance to seed market (needs %d, has %d)", totalSeedRequired, houseBalance)
		}
		_, err = tx.Exec(`UPDATE users SET balance = balance - $1 WHERE id = $2`, totalSeedRequired, HouseUserID)
		if err != nil {
			return nil, err
		}
	}

	// 3. Insert Pools & Seed Bets
	for _, outcome := range outcomes {
		// Create Pool
		queryPool := `
			INSERT INTO pools (market_id, outcome, total)
			VALUES ($1, $2, $3)
		`
		_, err = tx.Exec(queryPool, marketID, outcome, seed)
		if err != nil {
			return nil, err
		}

		// Create Seed Bet from house user if seed > 0
		if seed > 0 {
			betID := uuid.New().String()
			queryBet := `
				INSERT INTO bets (id, market_id, user_id, original_user_id, outcome, stake, created_at)
				VALUES ($1, $2, $3, $3, $4, $5, NOW())
			`
			_, err = tx.Exec(queryBet, betID, marketID, HouseUserID, outcome, seed)
			if err != nil {
				return nil, err
			}
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	market := &models.Market{
		ID:        marketID,
		Question:  question,
		Outcomes:  outcomes,
		Status:    models.StatusOpen,
		Seed:      seed,
		CreatedAt: timeNow(),
	}

	// Compute & Cache Live Odds
	details, err := CalculateAndCacheOdds(marketID)
	if err == nil {
		cache.PublishMarketUpdate(marketID, "market_created", details)
	}

	return market, nil
}

// PlaceBet places a user bet inside an atomic transaction
func PlaceBet(userID string, marketID string, outcome string, stake int64) (*models.Bet, error) {
	if stake <= 0 {
		return nil, errors.New("stake must be greater than 0")
	}

	tx, err := db.DB.BeginTx(context.Background(), nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	// 1. Get Market and Lock it in FOR SHARE mode to prevent status transitions (like lock/resolve) during bet placement
	var status models.MarketStatus
	var outcomesBytes []byte
	queryMarket := `SELECT status, outcomes FROM markets WHERE id = $1 FOR SHARE`
	err = tx.QueryRow(queryMarket, marketID).Scan(&status, &outcomesBytes)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errors.New("market not found")
		}
		return nil, err
	}

	if status != models.StatusOpen {
		return nil, ErrMarketNotOpen
	}

	var outcomes []string
	if err := json.Unmarshal(outcomesBytes, &outcomes); err != nil {
		return nil, err
	}

	// Validate outcome exists in market outcomes
	validOutcome := false
	for _, o := range outcomes {
		if o == outcome {
			validOutcome = true
			break
		}
	}
	if !validOutcome {
		return nil, ErrInvalidOutcome
	}

	// 2. Get and Lock User Balance
	var balance int64
	queryUser := `SELECT balance FROM users WHERE id = $1 FOR UPDATE`
	err = tx.QueryRow(queryUser, userID).Scan(&balance)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errors.New("user not found")
		}
		return nil, err
	}

	if balance < stake {
		return nil, ErrInsufficientBalance
	}

	// 3. Debit User Balance
	_, err = tx.Exec(`UPDATE users SET balance = balance - $1 WHERE id = $2`, stake, userID)
	if err != nil {
		return nil, err
	}

	// 4. Update Pool Total
	_, err = tx.Exec(`UPDATE pools SET total = total + $1 WHERE market_id = $2 AND outcome = $3`, stake, marketID, outcome)
	if err != nil {
		return nil, err
	}

	// 5. Insert Bet Record
	betID := uuid.New().String()
	queryInsertBet := `
		INSERT INTO bets (id, market_id, user_id, original_user_id, outcome, stake, created_at)
		VALUES ($1, $2, $3, $3, $4, $5, NOW())
	`
	_, err = tx.Exec(queryInsertBet, betID, marketID, userID, outcome, stake)
	if err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	bet := &models.Bet{
		ID:        betID,
		MarketID:  marketID,
		UserID:    userID,
		Outcome:   outcome,
		Stake:     stake,
		CreatedAt: timeNow(),
	}

	// Update Live Odds cache and pub/sub
	details, err := CalculateAndCacheOdds(marketID)
	if err == nil {
		cache.PublishMarketUpdate(marketID, "bet_placed", map[string]interface{}{
			"bet":     bet,
			"details": details,
		})
	}

	return bet, nil
}

// LockMarket locks the market to block future bets
func LockMarket(marketID string) error {
	tx, err := db.DB.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var status models.MarketStatus
	err = tx.QueryRow(`SELECT status FROM markets WHERE id = $1 FOR UPDATE`, marketID).Scan(&status)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("market not found")
		}
		return err
	}

	if status != models.StatusOpen {
		return fmt.Errorf("%w: current status is %s", ErrInvalidStateTransition, status)
	}

	// Bulk-invalidate any still-LISTED bet listings on this market - a
	// listing is only valid while its market is OPEN (see
	// listing_service.go). This can't affect balances/pools, it's a status
	// update on an entirely separate table.
	if _, err = tx.Exec(`UPDATE bet_listings SET status = 'CANCELLED', cancelled_at = NOW() WHERE market_id = $1 AND status = 'LISTED'`, marketID); err != nil {
		return err
	}

	_, err = tx.Exec(`UPDATE markets SET status = 'LOCKED' WHERE id = $1`, marketID)
	if err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return err
	}

	details, err := GetMarketDetails(marketID)
	if err == nil {
		cache.PublishMarketUpdate(marketID, "market_locked", details)
	}

	return nil
}

// ResolveMarket resolves the market and distributes the pool among winning bets (incorporating 3% commission)
func ResolveMarket(marketID string, winningOutcome string) error {
	tx, err := db.DB.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// 1. Get and Lock Market
	var status models.MarketStatus
	var outcomesBytes []byte
	err = tx.QueryRow(`SELECT status, outcomes FROM markets WHERE id = $1 FOR UPDATE`, marketID).Scan(&status, &outcomesBytes)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("market not found")
		}
		return err
	}

	// Why not switch case here? I feel like it would be better
	if status == models.StatusResolved {
		return ErrMarketAlreadyResolved
	}
	if status == models.StatusCancelled {
		return ErrMarketAlreadyCancelled
	}
	if status != models.StatusLocked {
		return ErrMarketNotLocked
	}

	// Bulk-invalidate any still-LISTED bet listings on this market (see the
	// same call in LockMarket - belt-and-suspenders, since LockMarket should
	// already have caught these, but resolve is the last stop before money
	// moves).
	if _, err = tx.Exec(`UPDATE bet_listings SET status = 'CANCELLED', cancelled_at = NOW() WHERE market_id = $1 AND status = 'LISTED'`, marketID); err != nil {
		return err
	}

	var outcomes []string
	if err := json.Unmarshal(outcomesBytes, &outcomes); err != nil {
		return err
	}

	// Validate outcome
	validOutcome := false
	for _, o := range outcomes {
		if o == winningOutcome {
			validOutcome = true
			break
		}
	}
	if !validOutcome {
		return ErrInvalidOutcome
	}

	// 2. Get Pools
	rows, err := tx.Query(`SELECT outcome, total FROM pools WHERE market_id = $1`, marketID)
	if err != nil {
		return err
	}
	defer rows.Close()

	var totalPool int64
	var poolVencedor int64
	pools := make(map[string]int64)

	// Yeah, lets see how this scales
	for rows.Next() {
		var o string
		var t int64
		if err := rows.Scan(&o, &t); err != nil {
			return err
		}
		pools[o] = t
		totalPool += t
		if o == winningOutcome {
			poolVencedor = t
		}
	}

	// Handle the edge case of pool_vencedor == 0
	if poolVencedor == 0 {
		// Revert to Cancellation/Refund if there is zero winning stake (including no seed)
		tx.Rollback()
		return RefundMarketInternal(marketID)
	}

	// 3. Calculate 3% House Commission (Rake) and Distributable Pool
	commission := totalPool * 3 / 100 // Integer division (floored)
	poolDistributable := totalPool - commission

	// 4. Query all winning bets
	betRows, err := tx.Query(`SELECT user_id, stake FROM bets WHERE market_id = $1 AND outcome = $2`, marketID, winningOutcome)
	if err != nil {
		return err
	}
	defer betRows.Close()

	type WinningBet struct {
		UserID string
		Stake  int64
	}
	var winningBets []WinningBet
	for betRows.Next() {
		var u string
		var s int64
		if err := betRows.Scan(&u, &s); err != nil {
			return err
		}
		winningBets = append(winningBets, WinningBet{UserID: u, Stake: s})
	}

	// 5. Distribute Payouts to Winners
	var totalPayoutsDistributed int64
	for _, wb := range winningBets {
		// payout = floor(stake * poolDistributable / poolVencedor)
		payout := wb.Stake * poolDistributable / poolVencedor
		totalPayoutsDistributed += payout

		// Credit User
		_, err = tx.Exec(`UPDATE users SET balance = balance + $1 WHERE id = $2`, payout, wb.UserID)
		if err != nil {
			return fmt.Errorf("failed to credit user %s: %w", wb.UserID, err)
		}
	}

	// 6. Handle Dust/Remainder and credit it + commission to the house
	remainder := poolDistributable - totalPayoutsDistributed
	totalHouseGain := commission + remainder

	if totalHouseGain > 0 {
		_, err = tx.Exec(`UPDATE users SET balance = balance + $1 WHERE id = $2`, totalHouseGain, HouseUserID)
		if err != nil {
			return fmt.Errorf("failed to credit house: %w", err)
		}
	}

	// 7. Update Market Status to RESOLVED
	_, err = tx.Exec(`UPDATE markets SET status = 'RESOLVED', winning_outcome = $1 WHERE id = $2`, winningOutcome, marketID)
	if err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return err
	}

	details, err := GetMarketDetails(marketID)
	if err == nil {
		cache.PublishMarketUpdate(marketID, "market_resolved", map[string]interface{}{
			"winning_outcome": winningOutcome,
			"commission":      commission,
			"remainder":       remainder,
			"details":         details,
		})
	}

	return nil
}

// CancelMarket cancels the market and refunds all stakes to users (including house seed)
func CancelMarket(marketID string) error {
	return RefundMarketInternal(marketID)
}

// RefundMarketInternal implements the cancel/refund transaction logic
func RefundMarketInternal(marketID string) error {
	tx, err := db.DB.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// 1. Get and Lock Market
	var status models.MarketStatus
	err = tx.QueryRow(`SELECT status FROM markets WHERE id = $1 FOR UPDATE`, marketID).Scan(&status)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("market not found")
		}
		return err
	}

	if status == models.StatusResolved {
		return ErrMarketAlreadyResolved
	}
	if status == models.StatusCancelled {
		return ErrMarketAlreadyCancelled
	}

	// Bulk-invalidate any still-LISTED bet listings on this market (see the
	// same call in LockMarket).
	if _, err = tx.Exec(`UPDATE bet_listings SET status = 'CANCELLED', cancelled_at = NOW() WHERE market_id = $1 AND status = 'LISTED'`, marketID); err != nil {
		return err
	}

	// 2. Fetch all bets
	rows, err := tx.Query(`SELECT user_id, stake FROM bets WHERE market_id = $1`, marketID)
	if err != nil {
		return err
	}
	defer rows.Close()

	type RefundBet struct {
		UserID string
		Stake  int64
	}
	var bets []RefundBet
	// yeah we need to be careful with this for rows
	for rows.Next() {
		var u string
		var s int64
		if err := rows.Scan(&u, &s); err != nil {
			return err
		}
		bets = append(bets, RefundBet{UserID: u, Stake: s})
	}

	// 3. Refund each bet to its owner (system seed bets automatically refund to HouseUserID)
	for _, b := range bets {
		_, err = tx.Exec(`UPDATE users SET balance = balance + $1 WHERE id = $2`, b.Stake, b.UserID)
		if err != nil {
			return fmt.Errorf("failed to refund user %s: %w", b.UserID, err)
		}
	}

	// 4. Update status to CANCELLED
	_, err = tx.Exec(`UPDATE markets SET status = 'CANCELLED' WHERE id = $1`, marketID)
	if err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return err
	}

	details, err := GetMarketDetails(marketID)
	if err == nil {
		cache.PublishMarketUpdate(marketID, "market_cancelled", details)
	}

	return nil
}

// GetMarketDetails fetches market status, outcome pool totals, and recalculates live odds
func GetMarketDetails(marketID string) (*models.MarketDetails, error) {
	var m models.Market
	var outcomesBytes []byte

	queryMarket := `SELECT id, question, outcomes, status, seed, winning_outcome, created_at FROM markets WHERE id = $1`
	err := db.DB.QueryRow(queryMarket, marketID).Scan(&m.ID, &m.Question, &outcomesBytes, &m.Status, &m.Seed, &m.WinningOutcome, &m.CreatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errors.New("market not found")
		}
		return nil, err
	}

	if err := json.Unmarshal(outcomesBytes, &m.Outcomes); err != nil {
		return nil, err
	}

	rows, err := db.DB.Query(`SELECT outcome, total FROM pools WHERE market_id = $1`, marketID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var totalPool int64
	pools := make(map[string]int64)

	// Initialize pools with 0 for all registered outcomes to avoid missing keys
	for _, outcome := range m.Outcomes {
		pools[outcome] = 0
	}

	for rows.Next() {
		var o string
		var t int64
		if err := rows.Scan(&o, &t); err != nil {
			return nil, err
		}
		pools[o] = t
		totalPool += t
	}

	// Calculate live odds
	liveOdds := make(map[string]float64)
	for outcome, total := range pools {
		if totalPool > 0 && total > 0 {
			// odds = pool_total / pool_outcome
			liveOdds[outcome] = float64(totalPool) / float64(total)
		} else {
			liveOdds[outcome] = 0.0
		}
	}

	return &models.MarketDetails{
		ID:             m.ID,
		Question:       m.Question,
		Outcomes:       m.Outcomes,
		Status:         m.Status,
		Seed:           m.Seed,
		WinningOutcome: m.WinningOutcome,
		Pools:          pools,
		TotalPool:      totalPool,
		LiveOdds:       liveOdds,
		CreatedAt:      m.CreatedAt,
	}, nil
}

// CalculateAndCacheOdds fetches latest state, calculates odds, caches to Redis and returns details
func CalculateAndCacheOdds(marketID string) (*models.MarketDetails, error) {
	details, err := GetMarketDetails(marketID)
	if err != nil {
		return nil, err
	}

	cache.CacheOdds(marketID, details.LiveOdds)
	return details, nil
}

// GetUser fetches a user's current balance - Phase 2 needs this to *read*
// balance (nav bar, profile page, post-bet winnings) without going through
// SyncUser, which is an upsert and would overwrite the balance as a
// side-effect of merely checking it.
func GetUser(userID string) (*models.User, error) {
	var u models.User
	err := db.DB.QueryRow(`SELECT id, balance, created_at FROM users WHERE id = $1`, userID).Scan(&u.ID, &u.Balance, &u.CreatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errors.New("user not found")
		}
		return nil, err
	}
	return &u, nil
}

// AdjustBalance atomically adds delta (may be negative) to a user's balance and
// returns the new balance. This is the ONLY safe way for an external caller
// (wallet-backend crediting a deposit / debiting a withdrawal) to move a
// balance: it locks the user row FOR UPDATE - exactly like PlaceBet does - so a
// concurrent bet/payout can't be lost to a read-modify-write race. A negative
// delta that would overdraw the balance is rejected with ErrInsufficientBalance
// and nothing is written, preserving the conservation invariant.
func AdjustBalance(userID string, delta int64) (int64, error) {
	tx, err := db.DB.BeginTx(context.Background(), nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	var balance int64
	err = tx.QueryRow(`SELECT balance FROM users WHERE id = $1 FOR UPDATE`, userID).Scan(&balance)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, errors.New("user not found")
		}
		return 0, err
	}

	newBalance := balance + delta
	if newBalance < 0 {
		return 0, ErrInsufficientBalance
	}

	if _, err := tx.Exec(`UPDATE users SET balance = $1 WHERE id = $2`, newBalance, userID); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return newBalance, nil
}

// ListMarkets returns every market with its current pools/live odds. Just N
// calls to the already-tested GetMarketDetails per id - simple beats clever
// at this scale, and it means the list view can never drift from the detail
// view's math since they're the exact same function.
func ListMarkets() ([]*models.MarketDetails, error) {
	rows, err := db.DB.Query(`SELECT id FROM markets ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}

	markets := make([]*models.MarketDetails, 0, len(ids))
	for _, id := range ids {
		details, err := GetMarketDetails(id)
		if err != nil {
			return nil, err
		}
		markets = append(markets, details)
	}

	return markets, nil
}

// ListMarketBets returns every bet on a market (including house seed bets)
// oldest first - general-backend replays this to reconstruct both the
// activity feed and a probability-history sparkline, so this is the one
// query that needs to exist rather than a separate odds-snapshot table.
func ListMarketBets(marketID string) ([]*models.Bet, error) {
	rows, err := db.DB.Query(`SELECT id, market_id, user_id, outcome, stake, created_at FROM bets WHERE market_id = $1 ORDER BY created_at ASC`, marketID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	bets := make([]*models.Bet, 0)
	for rows.Next() {
		var b models.Bet
		if err := rows.Scan(&b.ID, &b.MarketID, &b.UserID, &b.Outcome, &b.Stake, &b.CreatedAt); err != nil {
			return nil, err
		}
		bets = append(bets, &b)
	}

	return bets, nil
}

// ListUserBets returns every bet a user has placed, newest first, for the
// profile page's bet history.
func ListUserBets(userID string) ([]*models.Bet, error) {
	rows, err := db.DB.Query(`SELECT id, market_id, user_id, outcome, stake, created_at FROM bets WHERE user_id = $1 ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	bets := make([]*models.Bet, 0)
	for rows.Next() {
		var b models.Bet
		if err := rows.Scan(&b.ID, &b.MarketID, &b.UserID, &b.Outcome, &b.Stake, &b.CreatedAt); err != nil {
			return nil, err
		}
		bets = append(bets, &b)
	}

	return bets, nil
}

// Helper to keep times consistent
func timeNow() time.Time {
	return time.Now().UTC()
}
