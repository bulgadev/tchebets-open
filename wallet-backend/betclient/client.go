// Package betclient is wallet-backend's link to bet-backend's balance ledger.
// wallet-backend never stores balances - it credits deposits and debits
// withdrawals here, always through the atomic AdjustBalance endpoint (a signed
// delta under a row lock), never a read-modify-write. Unlike general-backend's
// betclient (a raw relay), this one decodes the balance because the deposit/
// withdrawal flow needs to know the resulting balance and distinguish an
// overdraft from a transport error.
package betclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// LedgerClient is the balance surface the service depends on. betclient.Client
// is the real implementation; service tests substitute a fake so the deposit/
// withdrawal orchestration is exercised without a live bet-backend.
type LedgerClient interface {
	AdjustBalance(ctx context.Context, userID string, delta int64) (newBalance int64, err error)
}

type Client struct {
	baseURL string
	http    *http.Client
}

func New(baseURL string) *Client {
	return &Client{
		baseURL: baseURL,
		http:    &http.Client{Timeout: 5 * time.Second},
	}
}

// ErrUnavailable is bet-backend being unreachable (timeout/refused) - the
// caller must NOT assume the adjust did or didn't happen.
var ErrUnavailable = fmt.Errorf("betting engine unavailable")

// ErrInsufficientBalance is a rejected debit (402) - the balance was too low.
// The ledger was not touched.
var ErrInsufficientBalance = fmt.Errorf("insufficient balance")

// ErrUserNotFound is a 404 - the account was never synced to bet-backend.
var ErrUserNotFound = fmt.Errorf("user not found")

type adjustResponse struct {
	Balance int64 `json:"balance"`
}

// AdjustBalance atomically moves userID's balance by delta and returns the new
// balance. delta > 0 credits (deposit), delta < 0 debits (withdrawal).
func (c *Client) AdjustBalance(ctx context.Context, userID string, delta int64) (int64, error) {
	body, err := json.Marshal(map[string]int64{"delta": delta})
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/users/"+userID+"/adjust", bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return 0, ErrUnavailable
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, ErrUnavailable
	}

	switch resp.StatusCode {
	case http.StatusOK:
		var r adjustResponse
		if err := json.Unmarshal(respBody, &r); err != nil {
			return 0, fmt.Errorf("decoding adjust response: %w", err)
		}
		return r.Balance, nil
	case http.StatusPaymentRequired:
		return 0, ErrInsufficientBalance
	case http.StatusNotFound:
		return 0, ErrUserNotFound
	default:
		return 0, fmt.Errorf("bet-backend adjust failed (status %d): %s", resp.StatusCode, string(respBody))
	}
}
