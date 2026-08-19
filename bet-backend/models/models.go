package models

import (
	"encoding/json"
	"time"
)

// MarketStatus represents the state of a market
type MarketStatus string

// Interesting approach for this file in general. Its good to have centralized objects in one place. 
// Also makes it easier to modify them later.

const ( // I like this approach, we can switch case this when we want to check the market state.
	StatusOpen      MarketStatus = "OPEN"
	StatusLocked    MarketStatus = "LOCKED"
	StatusResolved  MarketStatus = "RESOLVED"
	StatusCancelled MarketStatus = "CANCELLED"
)

// User represents the users table row
type User struct {
	ID        string    `json:"id" db:"id"`
	Balance   int64     `json:"balance" db:"balance"`
	CreatedAt time.Time `json:"created_at" db:"created_at"`
}

// Market represents the markets table row
type Market struct {
	ID             string       `json:"id" db:"id"`
	Question       string       `json:"question" db:"question"`
	Outcomes       []string     `json:"outcomes"`
	Status         MarketStatus `json:"status" db:"status"`
	Seed           int64        `json:"seed" db:"seed"`
	WinningOutcome *string      `json:"winning_outcome,omitempty" db:"winning_outcome"`
	CreatedAt      time.Time    `json:"created_at" db:"created_at"`
}

// Pool represents the pools table row
type Pool struct {
	MarketID string `json:"market_id" db:"market_id"`
	Outcome  string `json:"outcome" db:"outcome"`
	Total    int64  `json:"total" db:"total"`
}

// Bet represents the bets table row (immutable, append-only). UserID is the
// CURRENT owner - a sale (see service/listing_service.go) reassigns it, and
// ResolveMarket/RefundMarketInternal always pay out to whatever UserID sits
// here at settlement time. OriginalUserID is set once at insert and never
// touched again, independent of resale.
type Bet struct {
	ID             string    `json:"id" db:"id"`
	MarketID       string    `json:"market_id" db:"market_id"`
	UserID         string    `json:"user_id" db:"user_id"`
	OriginalUserID string    `json:"original_user_id" db:"original_user_id"`
	Outcome        string    `json:"outcome" db:"outcome"`
	Stake          int64     `json:"stake" db:"stake"`
	CreatedAt      time.Time `json:"created_at" db:"created_at"`
}

// BetListingStatus represents the state of a bet_listings row.
type BetListingStatus string

const (
	ListingStatusListed    BetListingStatus = "LISTED"
	ListingStatusSold      BetListingStatus = "SOLD"
	ListingStatusCancelled BetListingStatus = "CANCELLED"
)

// BetListing represents the bet_listings table row.
type BetListing struct {
	ID           string           `json:"id" db:"id"`
	BetID        string           `json:"bet_id" db:"bet_id"`
	MarketID     string           `json:"market_id" db:"market_id"`
	SellerID     string           `json:"seller_id" db:"seller_id"`
	BuyerID      *string          `json:"buyer_id,omitempty" db:"buyer_id"`
	AskingPrice  int64            `json:"asking_price" db:"asking_price"`
	Status       BetListingStatus `json:"status" db:"status"`
	CreatedAt    time.Time        `json:"created_at" db:"created_at"`
	SoldAt       *time.Time       `json:"sold_at,omitempty" db:"sold_at"`
	CancelledAt  *time.Time       `json:"cancelled_at,omitempty" db:"cancelled_at"`
}

// BetListingDetails is a BetListing enriched with the underlying bet's
// outcome/stake, saving general-backend an extra per-listing call.
type BetListingDetails struct {
	BetListing
	Outcome string `json:"outcome"`
	Stake   int64  `json:"stake"`
}

// MarketDetails represents the detailed view of a market including its pools and live odds
type MarketDetails struct {
	ID             string             `json:"id"`
	Question       string             `json:"question"`
	Outcomes       []string           `json:"outcomes"`
	Status         MarketStatus       `json:"status"`
	Seed           int64              `json:"seed"`
	WinningOutcome *string            `json:"winning_outcome,omitempty"`
	Pools          map[string]int64   `json:"pools"`
	TotalPool      int64              `json:"total_pool"`
	LiveOdds       map[string]float64 `json:"live_odds"`
	CreatedAt      time.Time          `json:"created_at"`
}

// OutcomesJSON is helper type for serialization of outcomes slice to/from Postgres JSONB
type OutcomesJSON []string

func (o OutcomesJSON) Value() (interface{}, error) {
	return json.Marshal(o)
}

func (o *OutcomesJSON) Scan(value interface{}) error {
	b, ok := value.([]byte)
	if !ok {
		return json.Unmarshal([]byte(value.(string)), o)
	}
	return json.Unmarshal(b, o)
}
