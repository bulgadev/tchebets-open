// This file is the real Swapper: SOL -> USDC via Jupiter's v6 aggregator API.
// It quotes a route (rejecting anything worse than maxSlippageBps), asks
// Jupiter to build the swap transaction for the account's own derived key,
// signs and broadcasts it, and credits the route's guaranteed minimum output
// (otherAmountThreshold) - never the optimistic quote - so the ledger can only
// ever under-credit a swap, never invent USDC.
//
// DEVNET NOTE: Jupiter only has mainnet liquidity. On devnet a quote returns no
// route and SwapSOLToUSDC errors, which surfaces as a FAILED deposit - the safe
// direction. The devnet-testable deposit path is direct USDC (no swap).
package chain

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/gagliardetto/solana-go"
	"github.com/gagliardetto/solana-go/rpc"

	"tchebet/wallet-backend/crypto"
)

const (
	jupiterQuoteURL = "https://quote-api.jup.ag/v6/quote"
	jupiterSwapURL  = "https://quote-api.jup.ag/v6/swap"
	// wrappedSOLMint is the SPL mint for native SOL, Jupiter's inputMint for a
	// SOL -> USDC route (wrapAndUnwrapSol handles the (un)wrapping).
	wrappedSOLMint = "So11111111111111111111111111111111111111112"
)

// JupiterSwapper is the live Swapper.
type JupiterSwapper struct {
	http     *http.Client
	rpc      *rpc.Client
	usdcMint string
}

var _ Swapper = (*JupiterSwapper)(nil)

func NewJupiterSwapper(rpcURL, usdcMint string) *JupiterSwapper {
	return &JupiterSwapper{
		http:     &http.Client{Timeout: 30 * time.Second},
		rpc:      rpc.New(rpcURL),
		usdcMint: usdcMint,
	}
}

type jupQuote struct {
	OutAmount            string `json:"outAmount"`
	OtherAmountThreshold string `json:"otherAmountThreshold"`
}

type jupSwapReq struct {
	QuoteResponse       json.RawMessage `json:"quoteResponse"`
	UserPublicKey       string          `json:"userPublicKey"`
	WrapAndUnwrapSol    bool            `json:"wrapAndUnwrapSol"`
	AsLegacyTransaction bool            `json:"asLegacyTransaction"`
}

type jupSwapResp struct {
	SwapTransaction string `json:"swapTransaction"`
}

// SwapSOLToUSDC quotes and executes a SOL -> USDC swap signed by `from`,
// returning the guaranteed-minimum micro-USDC output and the swap signature.
func (j *JupiterSwapper) SwapSOLToUSDC(ctx context.Context, from crypto.Keypair, lamports uint64, maxSlippageBps int) (uint64, string, error) {
	rawQuote, quote, err := j.quote(ctx, lamports, maxSlippageBps)
	if err != nil {
		return 0, "", err
	}
	minOut, err := strconv.ParseUint(quote.OtherAmountThreshold, 10, 64)
	if err != nil {
		return 0, "", fmt.Errorf("parsing quote threshold %q: %w", quote.OtherAmountThreshold, err)
	}

	b64, err := j.buildSwapTx(ctx, rawQuote, from.Address())
	if err != nil {
		return 0, "", err
	}

	tx, err := solana.TransactionFromBase64(b64)
	if err != nil {
		return 0, "", fmt.Errorf("decoding swap transaction: %w", err)
	}
	priv := solana.PrivateKey(from.PrivateKeyBytes())
	fromPub := solana.MustPublicKeyFromBase58(from.Address())
	if _, err := tx.Sign(func(key solana.PublicKey) *solana.PrivateKey {
		if key.Equals(fromPub) {
			return &priv
		}
		return nil
	}); err != nil {
		return 0, "", fmt.Errorf("signing swap transaction: %w", err)
	}

	sig, err := j.rpc.SendTransaction(ctx, tx)
	if err != nil {
		return 0, "", fmt.Errorf("broadcasting swap: %w", err)
	}
	if err := confirmSignature(ctx, j.rpc, sig); err != nil {
		return 0, sig.String(), err
	}
	return minOut, sig.String(), nil
}

func (j *JupiterSwapper) quote(ctx context.Context, lamports uint64, maxSlippageBps int) (json.RawMessage, *jupQuote, error) {
	q := url.Values{}
	q.Set("inputMint", wrappedSOLMint)
	q.Set("outputMint", j.usdcMint)
	q.Set("amount", strconv.FormatUint(lamports, 10))
	q.Set("slippageBps", strconv.Itoa(maxSlippageBps))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, jupiterQuoteURL+"?"+q.Encode(), nil)
	if err != nil {
		return nil, nil, err
	}
	body, err := j.do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("jupiter quote: %w", err)
	}
	var parsed jupQuote
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, nil, fmt.Errorf("decoding quote: %w", err)
	}
	if parsed.OtherAmountThreshold == "" {
		return nil, nil, fmt.Errorf("jupiter returned no route (SOL->USDC unavailable, e.g. on devnet)")
	}
	return json.RawMessage(body), &parsed, nil
}

func (j *JupiterSwapper) buildSwapTx(ctx context.Context, rawQuote json.RawMessage, userPubkey string) (string, error) {
	payload, err := json.Marshal(jupSwapReq{
		QuoteResponse:       rawQuote,
		UserPublicKey:       userPubkey,
		WrapAndUnwrapSol:    true,
		AsLegacyTransaction: true, // legacy tx so solana-go can decode+sign it
	})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, jupiterSwapURL, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	body, err := j.do(req)
	if err != nil {
		return "", fmt.Errorf("jupiter swap: %w", err)
	}
	var parsed jupSwapResp
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("decoding swap response: %w", err)
	}
	if parsed.SwapTransaction == "" {
		return "", fmt.Errorf("jupiter returned an empty swap transaction")
	}
	return parsed.SwapTransaction, nil
}

func (j *JupiterSwapper) do(req *http.Request) ([]byte, error) {
	resp, err := j.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, string(body))
	}
	return body, nil
}
