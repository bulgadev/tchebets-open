// Package betclient is the only thing in general-backend that talks to
// bet-backend. It doesn't need to understand bet-backend's response shapes in
// detail - once a request passes sanitize, the response just gets relayed
// back to the frontend as-is, so this stays a raw bytes+status proxy instead
// of decoding into duplicated model structs.

// (Good stuff man, love to see you are catching up with the my code style)
package betclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

type Client struct {
	baseURL string
	http    *http.Client
}

// _URL, defaulting to the docker
// network hostname since that's how this runs in compose.
func New() *Client {
	return &Client{
		baseURL: getEnv("BET_BACKEND_URL", "http://bet-backend:8080"),
		http:    &http.Client{Timeout: 5 * time.Second},
	}
}

// Response is what every betclient call returns: bet-backend's status code
// and raw body, ready to relay straight back to the frontend.
type Response struct {
	StatusCode int
	Body       []byte
}

// ErrUnavailable is returned when bet-backend itself couldn't be reached at
// all (timeout, connection refused) - distinct from bet-backend responding
// with an error status, which is just relayed as a normal Response.
var ErrUnavailable = fmt.Errorf("betting engine unavailable")

func (c *Client) do(ctx context.Context, method, path string, body interface{}) (*Response, error) {
	var reqBody io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal request body: %w", err)
		}
		reqBody = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to build request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, ErrUnavailable
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, ErrUnavailable
	}

	return &Response{StatusCode: resp.StatusCode, Body: respBody}, nil
}

func (c *Client) SyncUser(ctx context.Context, id string, balance int64) (*Response, error) {
	return c.do(ctx, http.MethodPost, "/users/sync", map[string]interface{}{
		"id":      id,
		"balance": balance,
	})
}

// GetUserRaw reads a user's balance without touching it - SyncUser is an
// upsert and would overwrite the balance as a side-effect of merely
// checking it, which is why this is a separate call.
func (c *Client) GetUserRaw(ctx context.Context, id string) (*Response, error) {
	return c.do(ctx, http.MethodGet, "/users/"+id, nil)
}

func (c *Client) CreateMarket(ctx context.Context, question string, outcomes []string, seed int64) (*Response, error) {
	return c.do(ctx, http.MethodPost, "/markets", map[string]interface{}{
		"question": question,
		"outcomes": outcomes,
		"seed":     seed,
	})
}

func (c *Client) GetMarket(ctx context.Context, marketID string) (*Response, error) {
	return c.do(ctx, http.MethodGet, "/markets/"+marketID, nil)
}

func (c *Client) ListMarketsRaw(ctx context.Context) (*Response, error) {
	return c.do(ctx, http.MethodGet, "/markets", nil)
}

func (c *Client) ListMarketBetsRaw(ctx context.Context, marketID string) (*Response, error) {
	return c.do(ctx, http.MethodGet, "/markets/"+marketID+"/bets", nil)
}

func (c *Client) ListUserBetsRaw(ctx context.Context, userID string) (*Response, error) {
	return c.do(ctx, http.MethodGet, "/users/"+userID+"/bets", nil)
}

func (c *Client) PlaceBet(ctx context.Context, marketID, userID, outcome string, stake int64) (*Response, error) {
	return c.do(ctx, http.MethodPost, "/markets/"+marketID+"/bet", map[string]interface{}{
		"user_id": userID,
		"outcome": outcome,
		"stake":   stake,
	})
}

func (c *Client) LockMarket(ctx context.Context, marketID string) (*Response, error) {
	return c.do(ctx, http.MethodPost, "/markets/"+marketID+"/lock", nil)
}

func (c *Client) ResolveMarket(ctx context.Context, marketID, winningOutcome string) (*Response, error) {
	return c.do(ctx, http.MethodPost, "/markets/"+marketID+"/resolve", map[string]interface{}{
		"winning_outcome": winningOutcome,
	})
}

func (c *Client) CancelMarket(ctx context.Context, marketID string) (*Response, error) {
	return c.do(ctx, http.MethodPost, "/markets/"+marketID+"/cancel", nil)
}

// ListBetForSale lists an existing bet as for-sale at a fixed asking price -
// bet-backend's secondary-market endpoints (Phase 3).
func (c *Client) ListBetForSale(ctx context.Context, betID, sellerID string, askingPrice int64) (*Response, error) {
	return c.do(ctx, http.MethodPost, "/bets/"+betID+"/listings", map[string]interface{}{
		"seller_id":    sellerID,
		"asking_price": askingPrice,
	})
}

func (c *Client) CancelListing(ctx context.Context, listingID, sellerID string) (*Response, error) {
	return c.do(ctx, http.MethodPost, "/listings/"+listingID+"/cancel", map[string]interface{}{
		"seller_id": sellerID,
	})
}

func (c *Client) BuyListing(ctx context.Context, listingID, buyerID string) (*Response, error) {
	return c.do(ctx, http.MethodPost, "/listings/"+listingID+"/buy", map[string]interface{}{
		"buyer_id": buyerID,
	})
}

func (c *Client) ListMarketListingsRaw(ctx context.Context, marketID string) (*Response, error) {
	return c.do(ctx, http.MethodGet, "/markets/"+marketID+"/listings", nil)
}

func (c *Client) ListUserListingsRaw(ctx context.Context, userID string) (*Response, error) {
	return c.do(ctx, http.MethodGet, "/users/"+userID+"/listings", nil)
}

func getEnv(key, fallback string) string {
	if value, exists := os.LookupEnv(key); exists {
		return value
	}
	return fallback
}