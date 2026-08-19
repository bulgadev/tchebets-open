// This file is the real DepositScanner: the polling replacement for a paid
// webhook provider. Each pass asks the RPC for signatures touching a watched
// account newer than the stored cursor, pulls each transaction, and reads the
// incoming amount from the pre/post balance deltas in the transaction meta
// (rather than parsing instruction layouts, which is brittle across program
// versions). Dedup is enforced downstream on deposits.signature, so an
// imprecise cursor only ever causes a harmless re-scan, never a double credit.
//
// DEVNET NOTE: only the USDC (SPL) path is exercisable on devnet - the SOL path
// exists for completeness but a SOL deposit only reaches the ledger via a
// Jupiter swap, and Jupiter has no devnet liquidity (see jupiter.go).
package chain

import (
	"context"
	"strconv"

	"github.com/gagliardetto/solana-go"
	"github.com/gagliardetto/solana-go/rpc"
)

// scanSignatureLimit bounds how many signatures a single pass pulls per address.
const scanSignatureLimit = 100

// SolanaScanner is the live DepositScanner.
type SolanaScanner struct {
	rpc      *rpc.Client
	usdcMint solana.PublicKey
}

var _ DepositScanner = (*SolanaScanner)(nil)

func NewSolanaScanner(rpcURL, usdcMint string) (*SolanaScanner, error) {
	mint, err := solana.PublicKeyFromBase58(usdcMint)
	if err != nil {
		return nil, err
	}
	return &SolanaScanner{rpc: rpc.New(rpcURL), usdcMint: mint}, nil
}

// ScanDeposits walks new signatures on both the base (SOL) address and the USDC
// ATA since sinceSig, returning the incoming transfers found and the newest
// signature seen (persist it as the next sinceSig).
func (s *SolanaScanner) ScanDeposits(ctx context.Context, baseAddress, usdcATA, sinceSig string) ([]DetectedDeposit, string, error) {
	base, err := solana.PublicKeyFromBase58(baseAddress)
	if err != nil {
		return nil, sinceSig, err
	}
	ata, err := solana.PublicKeyFromBase58(usdcATA)
	if err != nil {
		return nil, sinceSig, err
	}

	// Gather candidate signatures from both watched accounts, dedup, and track
	// the newest (highest slot) as the cursor to persist.
	seen := map[solana.Signature]bool{}
	var candidates []solana.Signature
	newCursor := sinceSig
	var newestSlot uint64

	for _, addr := range []solana.PublicKey{base, ata} {
		sigs, err := s.signaturesSince(ctx, addr, sinceSig)
		if err != nil {
			return nil, sinceSig, err
		}
		for _, ts := range sigs {
			if ts.Err != nil { // failed transaction - never a credit
				continue
			}
			if !seen[ts.Signature] {
				seen[ts.Signature] = true
				candidates = append(candidates, ts.Signature)
			}
			if ts.Slot > newestSlot {
				newestSlot = ts.Slot
				newCursor = ts.Signature.String()
			}
		}
	}

	var found []DetectedDeposit
	for _, sig := range candidates {
		deps, err := s.depositsInTx(ctx, sig, base, ata)
		if err != nil {
			return nil, sinceSig, err
		}
		found = append(found, deps...)
	}
	return found, newCursor, nil
}

// signaturesSince returns signatures on an address newer than sinceSig
// (newest-first), bounded by scanSignatureLimit.
func (s *SolanaScanner) signaturesSince(ctx context.Context, addr solana.PublicKey, sinceSig string) ([]*rpc.TransactionSignature, error) {
	limit := scanSignatureLimit
	opts := &rpc.GetSignaturesForAddressOpts{
		Limit:      &limit,
		Commitment: rpc.CommitmentConfirmed,
	}
	if sinceSig != "" {
		until, err := solana.SignatureFromBase58(sinceSig)
		if err == nil {
			opts.Until = until
		}
	}
	return s.rpc.GetSignaturesForAddressWithOpts(ctx, addr, opts)
}

// depositsInTx reads the incoming USDC and SOL amounts for the watched accounts
// out of one transaction's balance deltas.
func (s *SolanaScanner) depositsInTx(ctx context.Context, sig solana.Signature, base, ata solana.PublicKey) ([]DetectedDeposit, error) {
	maxVer := uint64(0)
	res, err := s.rpc.GetTransaction(ctx, sig, &rpc.GetTransactionOpts{
		Encoding:                       solana.EncodingBase64,
		Commitment:                     rpc.CommitmentConfirmed,
		MaxSupportedTransactionVersion: &maxVer,
	})
	if err != nil {
		return nil, err
	}
	if res == nil || res.Meta == nil || res.Meta.Err != nil || res.Transaction == nil {
		return nil, nil
	}
	tx, err := res.Transaction.GetTransaction()
	if err != nil {
		return nil, err
	}
	keys := tx.Message.AccountKeys

	var out []DetectedDeposit

	// USDC: delta of the ATA's token balance for our mint.
	if ataIdx := indexOf(keys, ata); ataIdx >= 0 {
		pre := tokenAmountAt(res.Meta.PreTokenBalances, ataIdx, s.usdcMint)
		post := tokenAmountAt(res.Meta.PostTokenBalances, ataIdx, s.usdcMint)
		if post > pre {
			out = append(out, DetectedDeposit{
				Signature: sig.String(),
				Asset:     "USDC",
				RawAmount: post - pre,
			})
		}
	}

	// SOL: native lamport delta on the base address.
	if baseIdx := indexOf(keys, base); baseIdx >= 0 &&
		baseIdx < len(res.Meta.PreBalances) && baseIdx < len(res.Meta.PostBalances) {
		pre := res.Meta.PreBalances[baseIdx]
		post := res.Meta.PostBalances[baseIdx]
		if post > pre {
			out = append(out, DetectedDeposit{
				Signature: sig.String(),
				Asset:     "SOL",
				RawAmount: post - pre,
			})
		}
	}
	return out, nil
}

func indexOf(keys []solana.PublicKey, target solana.PublicKey) int {
	for i, k := range keys {
		if k.Equals(target) {
			return i
		}
	}
	return -1
}

// tokenAmountAt returns the raw token amount for the given account index and
// mint from a token-balance slice, or 0 if absent.
func tokenAmountAt(bals []rpc.TokenBalance, idx int, mint solana.PublicKey) uint64 {
	for _, b := range bals {
		if int(b.AccountIndex) == idx && b.Mint.Equals(mint) && b.UiTokenAmount != nil {
			v, _ := strconv.ParseUint(b.UiTokenAmount.Amount, 10, 64)
			return v
		}
	}
	return 0
}
