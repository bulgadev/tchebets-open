// Package walletclient is general-backend's link to wallet-backend (the
// custodial crypto edge), used only when DEPOSIT_MODE=crypto. It mirrors the
// betclient pattern: a thin typed HTTP client over wallet-backend's internal
// API. general-backend stays R$-native - it sends/receives whole-BRL amounts
// and never sees micro-USDC; wallet-backend does the on-chain conversion.
package walletclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

type Client struct {
	baseURL string
	http    *http.Client
}

func New(baseURL string) *Client {
	// 60s, not 10s: provision and withdrawal do on-chain work that must be
	// confirmed before the call returns (ATA creation, USDC transfer), which
	// exceeds 10s on public devnet. A 10s cap made provision time out before the
	// chain confirmed, so it could never succeed.
	return &Client{baseURL: baseURL, http: &http.Client{Timeout: 60 * time.Second}}
}

// ErrManualReview is a 202: the withdrawal was accepted and debited but parked
// for an operator instead of auto-paid (above the manual-review threshold).
var ErrManualReview = fmt.Errorf("withdrawal parked for manual review")

// ErrInsufficientBalance is a 402 - the debit was rejected, balance untouched.
var ErrInsufficientBalance = fmt.Errorf("insufficient balance")

// ErrFaucetAlreadyClaimed is a 409 - the account already drew its one-time
// devnet demo grant.
var ErrFaucetAlreadyClaimed = fmt.Errorf("faucet already claimed")

// ErrDepositAddressNotFound means the account has not created a deposit target
// yet. It is distinct from a wallet-backend outage so the profile can offer the
// create-address action without treating a normal first visit as an error.
var ErrDepositAddressNotFound = fmt.Errorf("deposit address not provisioned")

// DepositAddress is an account's provisioned deposit target.
type DepositAddress struct {
	AccountID string `json:"account_id"`
	Address   string `json:"address"`
	USDCATA   string `json:"usdc_ata"`
}

// Deposit / Withdrawal mirror wallet-backend's ledger rows for the status list.
type Deposit struct {
	Signature     string `json:"signature"`
	Asset         string `json:"asset"`
	RawAmount     int64  `json:"raw_amount"`
	CreditedUSDC  int64  `json:"credited_usdc"`
	CreditedReais int64  `json:"credited_reais"`
	Status        string `json:"status"`
}

type Withdrawal struct {
	ID          string  `json:"id"`
	DestAddress string  `json:"dest_address"`
	AmountReais int64   `json:"amount_reais"`
	AmountUSDC  int64   `json:"amount_usdc"`
	Status      string  `json:"status"`
	Signature   *string `json:"signature"`
}

// ProvisionAccount returns (creating on first call) the account's deposit address.
func (c *Client) ProvisionAccount(ctx context.Context, accountID string) (*DepositAddress, error) {
	var out DepositAddress
	if err := c.do(ctx, http.MethodPost, "/accounts/"+accountID+"/provision", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// LookupDepositAddress reads a previously provisioned deposit target. It never
// triggers the on-chain ATA creation that ProvisionAccount performs.
func (c *Client) LookupDepositAddress(ctx context.Context, accountID string) (*DepositAddress, error) {
	var out DepositAddress
	status, err := c.doStatus(ctx, http.MethodGet, "/accounts/"+accountID+"/address", nil, &out)
	if err != nil {
		return nil, err
	}
	switch status {
	case http.StatusOK:
		return &out, nil
	case http.StatusNotFound:
		return nil, ErrDepositAddressNotFound
	default:
		return nil, fmt.Errorf("wallet-backend GET deposit address: status %d", status)
	}
}

// Faucet asks wallet-backend to send the account its one-time devnet USDC demo
// grant (from treasury). Returns the on-chain signature; the grant is credited
// by the deposit poller, so the balance updates on the next status poll. A 409
// surfaces as ErrFaucetAlreadyClaimed (already drew its grant).
func (c *Client) Faucet(ctx context.Context, accountID string) (string, error) {
	var out struct {
		Signature string `json:"signature"`
	}
	status, err := c.doStatus(ctx, http.MethodPost, "/accounts/"+accountID+"/faucet", nil, &out)
	if err != nil {
		return "", err
	}
	switch status {
	case http.StatusOK:
		return out.Signature, nil
	case http.StatusConflict:
		return "", ErrFaucetAlreadyClaimed
	default:
		return "", fmt.Errorf("wallet-backend faucet failed (status %d)", status)
	}
}

type withdrawBody struct {
	DestAddress string `json:"dest_address"`
	AmountReais int64  `json:"amount_reais"`
}

// RequestWithdrawal asks wallet-backend to debit amountReais and pay out USDC.
// A 202 (parked for review) surfaces as ErrManualReview - the debit still
// happened, so the caller should tell the user it's queued, not failed.
func (c *Client) RequestWithdrawal(ctx context.Context, accountID, dest string, amountReais int64) (*Withdrawal, error) {
	body, _ := json.Marshal(withdrawBody{DestAddress: dest, AmountReais: amountReais})
	var out Withdrawal
	status, err := c.doStatus(ctx, http.MethodPost, "/accounts/"+accountID+"/withdraw", body, &out)
	if err != nil {
		return nil, err
	}
	switch status {
	case http.StatusOK:
		return &out, nil
	case http.StatusAccepted:
		return &out, ErrManualReview
	case http.StatusPaymentRequired:
		return nil, ErrInsufficientBalance
	default:
		return nil, fmt.Errorf("wallet-backend withdraw failed (status %d)", status)
	}
}

func (c *Client) ListDeposits(ctx context.Context, accountID string) ([]Deposit, error) {
	var out struct {
		Deposits []Deposit `json:"deposits"`
	}
	if err := c.do(ctx, http.MethodGet, "/accounts/"+accountID+"/deposits", nil, &out); err != nil {
		return nil, err
	}
	return out.Deposits, nil
}

func (c *Client) ListWithdrawals(ctx context.Context, accountID string) ([]Withdrawal, error) {
	var out struct {
		Withdrawals []Withdrawal `json:"withdrawals"`
	}
	if err := c.do(ctx, http.MethodGet, "/accounts/"+accountID+"/withdrawals", nil, &out); err != nil {
		return nil, err
	}
	return out.Withdrawals, nil
}

// do performs a request and decodes a 2xx JSON body, treating any non-2xx as an
// error. doStatus is the variant that hands the status code back for the
// withdraw path's 202/402 branching.
func (c *Client) do(ctx context.Context, method, path string, body []byte, out any) error {
	status, err := c.doStatus(ctx, method, path, body, out)
	if err != nil {
		return err
	}
	if status < 200 || status >= 300 {
		return fmt.Errorf("wallet-backend %s %s: status %d", method, path, status)
	}
	return nil
}

func (c *Client) doStatus(ctx context.Context, method, path string, body []byte, out any) (int, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return 0, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, fmt.Errorf("wallet-backend unreachable: %w", err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, err
	}
	if out != nil && len(respBody) > 0 && resp.StatusCode >= 200 && resp.StatusCode < 300 {
		if err := json.Unmarshal(respBody, out); err != nil {
			return resp.StatusCode, fmt.Errorf("decoding wallet-backend response: %w", err)
		}
	}
	return resp.StatusCode, nil
}
