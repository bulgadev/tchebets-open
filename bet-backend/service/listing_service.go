package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"tchebet/bet-backend/cache"
	"tchebet/bet-backend/db"
	"tchebet/bet-backend/models"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

// Sentinel errors for the bet-resale (secondary market) feature. Reuses
// ErrMarketNotOpen (400) and ErrInsufficientBalance (402) from
// market_service.go where the scenario is identical to PlaceBet's.
var ErrBetNotFound = errors.New("bet not found")
var ErrListingNotFound = errors.New("listing not found")
var ErrNotBetOwner = errors.New("you do not own this bet")
var ErrNotListingOwner = errors.New("you do not own this listing")
var ErrCannotListHouseBet = errors.New("house seed bets cannot be listed for sale")
var ErrBetAlreadyListed = errors.New("this bet already has an active listing")
var ErrListingNotActive = errors.New("this listing is no longer active")
var ErrCannotBuyOwnListing = errors.New("you cannot buy your own listing")
var ErrListingMarketNoLongerOpen = errors.New("the market for this listing is no longer open")

// Lock ordering contract for every function in this file, to prevent
// cross-function deadlocks with market_service.go's existing locks:
// markets -> bet_listings -> bets -> users (ascending UUID-string order when
// two users rows are locked in the same transaction).

// ListBetForSale lists an existing bet as for-sale at a fixed asking price.
// Whole-position only, no partial-stake splitting.
func ListBetForSale(betID, sellerID string, askingPrice int64) (*models.BetListingDetails, error) {
	if askingPrice <= 0 {
		return nil, errors.New("asking price must be greater than 0")
	}

	tx, err := db.DB.BeginTx(context.Background(), nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	// Unlocked pre-read to discover market_id before locking markets first.
	var marketID string
	if err := tx.QueryRow(`SELECT market_id FROM bets WHERE id = $1`, betID).Scan(&marketID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrBetNotFound
		}
		return nil, err
	}

	var marketStatus models.MarketStatus
	if err := tx.QueryRow(`SELECT status FROM markets WHERE id = $1 FOR SHARE`, marketID).Scan(&marketStatus); err != nil {
		return nil, err
	}
	if marketStatus != models.StatusOpen {
		return nil, ErrMarketNotOpen
	}

	var ownerID, outcome string
	var stake int64
	err = tx.QueryRow(`SELECT user_id, outcome, stake FROM bets WHERE id = $1 FOR UPDATE`, betID).Scan(&ownerID, &outcome, &stake)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrBetNotFound
		}
		return nil, err
	}
	if ownerID != sellerID {
		return nil, ErrNotBetOwner
	}
	if sellerID == HouseUserID {
		return nil, ErrCannotListHouseBet
	}

	listingID := uuid.New().String()
	_, err = tx.Exec(
		`INSERT INTO bet_listings (id, bet_id, market_id, seller_id, asking_price, status, created_at)
		 VALUES ($1, $2, $3, $4, $5, 'LISTED', NOW())`,
		listingID, betID, marketID, sellerID, askingPrice,
	)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, ErrBetAlreadyListed
		}
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	return &models.BetListingDetails{
		BetListing: models.BetListing{
			ID:          listingID,
			BetID:       betID,
			MarketID:    marketID,
			SellerID:    sellerID,
			AskingPrice: askingPrice,
			Status:      models.ListingStatusListed,
			CreatedAt:   timeNow(),
		},
		Outcome: outcome,
		Stake:   stake,
	}, nil
}

// CancelListing withdraws an active listing - only its seller may do this,
// and only while it's still LISTED (this status check + row lock is exactly
// what makes "seller cancels mid-purchase" race-safe against BuyListedBet).
func CancelListing(listingID, sellerID string) error {
	tx, err := db.DB.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var actualSellerID string
	var status models.BetListingStatus
	err = tx.QueryRow(`SELECT seller_id, status FROM bet_listings WHERE id = $1 FOR UPDATE`, listingID).Scan(&actualSellerID, &status)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrListingNotFound
		}
		return err
	}
	if actualSellerID != sellerID {
		return ErrNotListingOwner
	}
	if status != models.ListingStatusListed {
		return ErrListingNotActive
	}

	if _, err := tx.Exec(`UPDATE bet_listings SET status = 'CANCELLED', cancelled_at = NOW() WHERE id = $1`, listingID); err != nil {
		return err
	}

	return tx.Commit()
}

// invalidateStaleListing cancels a single listing whose market has moved on
// since it was created - run as its own short transaction, separate from the
// read-only lookup in BuyListedBet that discovered the staleness, mirroring
// the existing "abort and redo in a clean follow-up transaction" idiom
// ResolveMarket already uses when it falls back to RefundMarketInternal.
func invalidateStaleListing(listingID string) error {
	tx, err := db.DB.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var status models.BetListingStatus
	if err := tx.QueryRow(`SELECT status FROM bet_listings WHERE id = $1 FOR UPDATE`, listingID).Scan(&status); err != nil {
		return err
	}
	if status == models.ListingStatusListed {
		if _, err := tx.Exec(`UPDATE bet_listings SET status = 'CANCELLED', cancelled_at = NOW() WHERE id = $1`, listingID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// BuyListedBet is the money-critical path: buyer pays seller directly for
// the listing's asking price, ownership of the underlying bet transfers to
// the buyer. The house account is never referenced anywhere in this
// function - a sale is pure peer-to-peer and must never touch it.
func BuyListedBet(listingID, buyerID string) (*models.BetListingDetails, error) {
	tx, err := db.DB.BeginTx(context.Background(), nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	// Unlocked pre-read to discover market_id before locking markets first.
	var marketID string
	if err := tx.QueryRow(`SELECT market_id FROM bet_listings WHERE id = $1`, listingID).Scan(&marketID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrListingNotFound
		}
		return nil, err
	}

	var marketStatus models.MarketStatus
	if err := tx.QueryRow(`SELECT status FROM markets WHERE id = $1 FOR SHARE`, marketID).Scan(&marketStatus); err != nil {
		return nil, err
	}

	var betID, sellerID string
	var askingPrice int64
	var status models.BetListingStatus
	err = tx.QueryRow(
		`SELECT bet_id, seller_id, asking_price, status FROM bet_listings WHERE id = $1 FOR UPDATE`,
		listingID,
	).Scan(&betID, &sellerID, &askingPrice, &status)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrListingNotFound
		}
		return nil, err
	}

	// Market-staleness is checked before the listing's own status: the two
	// proactive bulk-cancels in LockMarket/ResolveMarket/RefundMarketInternal
	// (see market_service.go) mean a listing on a no-longer-OPEN market is
	// usually already flipped to CANCELLED by the time we get here, which
	// would otherwise report the less informative ErrListingNotActive. This
	// ordering makes "the market isn't open anymore" the reported reason
	// whenever it's the actual cause, whether or not the proactive cancel
	// already ran.
	if marketStatus != models.StatusOpen {
		tx.Rollback()
		if err := invalidateStaleListing(listingID); err != nil {
			return nil, err
		}
		return nil, ErrListingMarketNoLongerOpen
	}
	if status != models.ListingStatusListed {
		return nil, ErrListingNotActive
	}

	if sellerID == buyerID {
		return nil, ErrCannotBuyOwnListing
	}

	var betOwnerID, outcome string
	var stake int64
	if err := tx.QueryRow(`SELECT user_id, outcome, stake FROM bets WHERE id = $1 FOR UPDATE`, betID).Scan(&betOwnerID, &outcome, &stake); err != nil {
		return nil, err
	}
	if betOwnerID != sellerID {
		// Internal-consistency check, not a normal user-facing error - if
		// this ever fires it means listing state and bet ownership drifted
		// apart somewhere else in the codebase.
		return nil, fmt.Errorf("listing %s's bet owner does not match its seller", listingID)
	}

	// Lock both user rows in ascending UUID-string order to avoid a two-row
	// deadlock against a concurrent reverse transaction (no existing
	// function locks two users rows at once, so this ordering is new here).
	firstID, secondID := buyerID, sellerID
	if secondID < firstID {
		firstID, secondID = secondID, firstID
	}
	var firstBalance, secondBalance int64
	if err := tx.QueryRow(`SELECT balance FROM users WHERE id = $1 FOR UPDATE`, firstID).Scan(&firstBalance); err != nil {
		return nil, err
	}
	if err := tx.QueryRow(`SELECT balance FROM users WHERE id = $1 FOR UPDATE`, secondID).Scan(&secondBalance); err != nil {
		return nil, err
	}
	var buyerBalance int64
	if firstID == buyerID {
		buyerBalance = firstBalance
	} else {
		buyerBalance = secondBalance
	}

	if buyerBalance < askingPrice {
		return nil, ErrInsufficientBalance
	}

	if _, err := tx.Exec(`UPDATE users SET balance = balance - $1 WHERE id = $2`, askingPrice, buyerID); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`UPDATE users SET balance = balance + $1 WHERE id = $2`, askingPrice, sellerID); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`UPDATE bets SET user_id = $1 WHERE id = $2`, buyerID, betID); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`UPDATE bet_listings SET status = 'SOLD', buyer_id = $1, sold_at = NOW() WHERE id = $2`, buyerID, listingID); err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	buyer := buyerID
	details, err := CalculateAndCacheOdds(marketID)
	if err == nil {
		cache.PublishMarketUpdate(marketID, "bet_sold", map[string]interface{}{
			"listing_id": listingID,
			"bet_id":     betID,
			"buyer_id":   buyerID,
			"seller_id":  sellerID,
			"details":    details,
		})
	}

	return &models.BetListingDetails{
		BetListing: models.BetListing{
			ID:          listingID,
			BetID:       betID,
			MarketID:    marketID,
			SellerID:    sellerID,
			BuyerID:     &buyer,
			AskingPrice: askingPrice,
			Status:      models.ListingStatusSold,
			SoldAt:      timeNowPtr(),
		},
		Outcome: outcome,
		Stake:   stake,
	}, nil
}

// ListActiveListingsForMarket returns every LISTED listing on a market, for
// the market page's "for sale" section.
func ListActiveListingsForMarket(marketID string) ([]*models.BetListingDetails, error) {
	return queryListingDetails(`
		SELECT l.id, l.bet_id, l.market_id, l.seller_id, l.buyer_id, l.asking_price, l.status, l.created_at, l.sold_at, l.cancelled_at, b.outcome, b.stake
		FROM bet_listings l JOIN bets b ON b.id = l.bet_id
		WHERE l.market_id = $1 AND l.status = 'LISTED'
		ORDER BY l.created_at ASC`, marketID)
}

// ListActiveListingsForSeller returns a seller's own LISTED listings, for the
// profile page's "already listed" state.
func ListActiveListingsForSeller(sellerID string) ([]*models.BetListingDetails, error) {
	return queryListingDetails(`
		SELECT l.id, l.bet_id, l.market_id, l.seller_id, l.buyer_id, l.asking_price, l.status, l.created_at, l.sold_at, l.cancelled_at, b.outcome, b.stake
		FROM bet_listings l JOIN bets b ON b.id = l.bet_id
		WHERE l.seller_id = $1 AND l.status = 'LISTED'
		ORDER BY l.created_at ASC`, sellerID)
}

func queryListingDetails(query string, arg string) ([]*models.BetListingDetails, error) {
	rows, err := db.DB.Query(query, arg)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	listings := make([]*models.BetListingDetails, 0)
	for rows.Next() {
		var l models.BetListingDetails
		if err := rows.Scan(
			&l.ID, &l.BetID, &l.MarketID, &l.SellerID, &l.BuyerID, &l.AskingPrice,
			&l.Status, &l.CreatedAt, &l.SoldAt, &l.CancelledAt, &l.Outcome, &l.Stake,
		); err != nil {
			return nil, err
		}
		listings = append(listings, &l)
	}
	return listings, nil
}

// isUniqueViolation checks for Postgres unique_violation (SQLSTATE 23505) -
// the partial unique index on bet_listings(bet_id) is what turns a double
// listing attempt into this.
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func timeNowPtr() *time.Time {
	t := timeNow()
	return &t
}
