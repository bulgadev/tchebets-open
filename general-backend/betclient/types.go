// types.go adds typed decode methods next to client.go's raw byte-relay
// ones. The raw methods are still exactly right for the routes that just
// forward bet-backend's response verbatim - but the page-rendering handlers
// (templ) need real Go structs to work with, not bytes, so these sit
// alongside rather than replacing anything.
package betclient

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"
)

var ErrMarketNotFound = errors.New("market not found")

// StatusError wraps a non-2xx bet-backend response so callers that need to
// branch on the exact status (e.g. 402 insufficient balance vs 400 bad
// outcome) can, via errors.As, instead of string-matching messages.
type StatusError struct {
	StatusCode int
	Message    string
}

func (e *StatusError) Error() string {
	return e.Message
}

type MarketDetails struct {
	ID             string             `json:"id"`
	Question       string             `json:"question"`
	Outcomes       []string           `json:"outcomes"`
	Status         string             `json:"status"`
	Seed           int64              `json:"seed"`
	WinningOutcome *string            `json:"winning_outcome,omitempty"`
	Pools          map[string]int64   `json:"pools"`
	TotalPool      int64              `json:"total_pool"`
	LiveOdds       map[string]float64 `json:"live_odds"`
	CreatedAt      time.Time          `json:"created_at"`
}

type Bet struct {
	ID        string    `json:"id"`
	MarketID  string    `json:"market_id"`
	UserID    string    `json:"user_id"`
	Outcome   string    `json:"outcome"`
	Stake     int64     `json:"stake"`
	CreatedAt time.Time `json:"created_at"`
}

type CreateMarketResponse struct {
	ID       string   `json:"id"`
	Question string   `json:"question"`
	Outcomes []string `json:"outcomes"`
	Status   string   `json:"status"`
	Seed     int64    `json:"seed"`
}

type SyncUserResponse struct {
	Status  string `json:"status"`
	UserID  string `json:"user_id"`
	Balance int64  `json:"balance"`
}

type User struct {
	ID        string    `json:"id"`
	Balance   int64     `json:"balance"`
	CreatedAt time.Time `json:"created_at"`
}

// bodyError is bet-backend's uniform {"error": "..."} response shape.
type bodyError struct {
	Error string `json:"error"`
}

// decodeErr turns a non-2xx Response into a Go error, special-casing 404 so
// callers can branch on ErrMarketNotFound, and otherwise wrapping the status
// + message in a *StatusError so callers can switch on StatusCode (402
// insufficient balance, 400 bad outcome/market not open, etc) instead of
// string-matching.
func decodeErr(resp *Response) error {
	if resp.StatusCode == http.StatusNotFound {
		return ErrMarketNotFound
	}
	var be bodyError
	message := string(resp.Body)
	if err := json.Unmarshal(resp.Body, &be); err == nil && be.Error != "" {
		message = be.Error
	}
	return &StatusError{StatusCode: resp.StatusCode, Message: message}
}

func (c *Client) GetMarketDetails(ctx context.Context, marketID string) (*MarketDetails, error) {
	resp, err := c.GetMarket(ctx, marketID)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, decodeErr(resp)
	}
	var details MarketDetails
	if err := json.Unmarshal(resp.Body, &details); err != nil {
		return nil, err
	}
	return &details, nil
}

func (c *Client) ListMarkets(ctx context.Context) ([]MarketDetails, error) {
	resp, err := c.ListMarketsRaw(ctx)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, decodeErr(resp)
	}
	var markets []MarketDetails
	if err := json.Unmarshal(resp.Body, &markets); err != nil {
		return nil, err
	}
	return markets, nil
}

func (c *Client) ListMarketBets(ctx context.Context, marketID string) ([]Bet, error) {
	resp, err := c.ListMarketBetsRaw(ctx, marketID)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, decodeErr(resp)
	}
	var bets []Bet
	if err := json.Unmarshal(resp.Body, &bets); err != nil {
		return nil, err
	}
	return bets, nil
}

func (c *Client) ListUserBets(ctx context.Context, userID string) ([]Bet, error) {
	resp, err := c.ListUserBetsRaw(ctx, userID)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, decodeErr(resp)
	}
	var bets []Bet
	if err := json.Unmarshal(resp.Body, &bets); err != nil {
		return nil, err
	}
	return bets, nil
}

func (c *Client) CreateMarketTyped(ctx context.Context, question string, outcomes []string, seed int64) (*CreateMarketResponse, error) {
	resp, err := c.CreateMarket(ctx, question, outcomes, seed)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusCreated {
		return nil, decodeErr(resp)
	}
	var out CreateMarketResponse
	if err := json.Unmarshal(resp.Body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) SyncUserTyped(ctx context.Context, id string, balance int64) (*SyncUserResponse, error) {
	resp, err := c.SyncUser(ctx, id, balance)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, decodeErr(resp)
	}
	var out SyncUserResponse
	if err := json.Unmarshal(resp.Body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) GetUser(ctx context.Context, id string) (*User, error) {
	resp, err := c.GetUserRaw(ctx, id)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, decodeErr(resp)
	}
	var u User
	if err := json.Unmarshal(resp.Body, &u); err != nil {
		return nil, err
	}
	return &u, nil
}

func (c *Client) PlaceBetTyped(ctx context.Context, marketID, userID, outcome string, stake int64) (*Bet, error) {
	resp, err := c.PlaceBet(ctx, marketID, userID, outcome, stake)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusCreated {
		return nil, decodeErr(resp)
	}
	var out Bet
	if err := json.Unmarshal(resp.Body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListingDetails mirrors bet-backend's models.BetListingDetails.
type ListingDetails struct {
	ID          string     `json:"id"`
	BetID       string     `json:"bet_id"`
	MarketID    string     `json:"market_id"`
	SellerID    string     `json:"seller_id"`
	BuyerID     *string    `json:"buyer_id,omitempty"`
	AskingPrice int64      `json:"asking_price"`
	Status      string     `json:"status"`
	CreatedAt   time.Time  `json:"created_at"`
	SoldAt      *time.Time `json:"sold_at,omitempty"`
	CancelledAt *time.Time `json:"cancelled_at,omitempty"`
	Outcome     string     `json:"outcome"`
	Stake       int64      `json:"stake"`
}

func (c *Client) ListBetForSaleTyped(ctx context.Context, betID, sellerID string, askingPrice int64) (*ListingDetails, error) {
	resp, err := c.ListBetForSale(ctx, betID, sellerID, askingPrice)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusCreated {
		return nil, decodeErr(resp)
	}
	var out ListingDetails
	if err := json.Unmarshal(resp.Body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) CancelListingTyped(ctx context.Context, listingID, sellerID string) error {
	resp, err := c.CancelListing(ctx, listingID, sellerID)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return decodeErr(resp)
	}
	return nil
}

func (c *Client) BuyListingTyped(ctx context.Context, listingID, buyerID string) (*ListingDetails, error) {
	resp, err := c.BuyListing(ctx, listingID, buyerID)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, decodeErr(resp)
	}
	var out ListingDetails
	if err := json.Unmarshal(resp.Body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) ListMarketListings(ctx context.Context, marketID string) ([]ListingDetails, error) {
	resp, err := c.ListMarketListingsRaw(ctx, marketID)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, decodeErr(resp)
	}
	var out []ListingDetails
	if err := json.Unmarshal(resp.Body, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) ListUserListings(ctx context.Context, userID string) ([]ListingDetails, error) {
	resp, err := c.ListUserListingsRaw(ctx, userID)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, decodeErr(resp)
	}
	var out []ListingDetails
	if err := json.Unmarshal(resp.Body, &out); err != nil {
		return nil, err
	}
	return out, nil
}
