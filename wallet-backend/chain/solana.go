// This file is the real, network-touching implementation of the Client
// interface, backed by gagliardetto/solana-go against a Solana JSON-RPC
// endpoint (devnet by default). It is deliberately kept separate from the
// interface + fakes (chain.go / service tests) because signing real-money
// transactions must be exercised end-to-end against devnet, not just compiled.
//
// Signing model: every transaction here has a single signer that is also the
// fee payer - the treasury keypair for ATA creation and withdrawals, or a
// user's derived key for a SOL sweep. crypto.Keypair carries the 64-byte
// ed25519 private key solana.PrivateKey expects, so no re-derivation is needed.
package chain

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/gagliardetto/solana-go"
	associatedtokenaccount "github.com/gagliardetto/solana-go/programs/associated-token-account"
	"github.com/gagliardetto/solana-go/programs/system"
	"github.com/gagliardetto/solana-go/programs/token"
	"github.com/gagliardetto/solana-go/rpc"

	"tchebet/wallet-backend/crypto"
)

// usdcDecimals is USDC's SPL mint decimal count - passed to TransferChecked so
// the token program itself rejects a wrong-mint/wrong-decimals transfer.
const usdcDecimals uint8 = 6

// confirmPollInterval is how often ConfirmTx re-checks signature status while
// waiting for finalization.
const confirmPollInterval = 2 * time.Second

// SolanaClient is the live chain.Client. Construct once at startup.
type SolanaClient struct {
	rpc      *rpc.Client
	usdcMint solana.PublicKey
}

// Compile-time proof it satisfies the interface the service depends on.
var _ Client = (*SolanaClient)(nil)

// NewSolanaClient dials the RPC endpoint and pins the USDC mint for the cluster.
func NewSolanaClient(rpcURL, usdcMint string) (*SolanaClient, error) {
	mint, err := solana.PublicKeyFromBase58(usdcMint)
	if err != nil {
		return nil, fmt.Errorf("invalid USDC mint %q: %w", usdcMint, err)
	}
	return &SolanaClient{rpc: rpc.New(rpcURL), usdcMint: mint}, nil
}

func (c *SolanaClient) ReadSOLBalance(ctx context.Context, address string) (uint64, error) {
	pk, err := solana.PublicKeyFromBase58(address)
	if err != nil {
		return 0, fmt.Errorf("invalid address %q: %w", address, err)
	}
	res, err := c.rpc.GetBalance(ctx, pk, rpc.CommitmentFinalized)
	if err != nil {
		return 0, err
	}
	return res.Value, nil
}

func (c *SolanaClient) ReadUSDCBalance(ctx context.Context, ownerAddress string) (uint64, error) {
	ataStr, err := c.DeriveUSDCATA(ownerAddress)
	if err != nil {
		return 0, err
	}
	ata := solana.MustPublicKeyFromBase58(ataStr)

	// If the ATA has never been created, the balance is simply zero - not an error.
	exists, err := c.accountExists(ctx, ata)
	if err != nil {
		return 0, err
	}
	if !exists {
		return 0, nil
	}

	res, err := c.rpc.GetTokenAccountBalance(ctx, ata, rpc.CommitmentFinalized)
	if err != nil {
		return 0, err
	}
	amount, err := strconv.ParseUint(res.Value.Amount, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parsing token amount %q: %w", res.Value.Amount, err)
	}
	return amount, nil
}

func (c *SolanaClient) DeriveUSDCATA(ownerAddress string) (string, error) {
	owner, err := solana.PublicKeyFromBase58(ownerAddress)
	if err != nil {
		return "", fmt.Errorf("invalid owner %q: %w", ownerAddress, err)
	}
	ata, _, err := solana.FindAssociatedTokenAddress(owner, c.usdcMint)
	if err != nil {
		return "", err
	}
	return ata.String(), nil
}

func (c *SolanaClient) EnsureUSDCATA(ctx context.Context, treasury crypto.Keypair, ownerAddress string) (string, string, error) {
	owner, err := solana.PublicKeyFromBase58(ownerAddress)
	if err != nil {
		return "", "", fmt.Errorf("invalid owner %q: %w", ownerAddress, err)
	}
	ata, _, err := solana.FindAssociatedTokenAddress(owner, c.usdcMint)
	if err != nil {
		return "", "", err
	}

	exists, err := c.accountExists(ctx, ata)
	if err != nil {
		return "", "", err
	}
	if exists {
		return ata.String(), "", nil
	}

	ix := associatedtokenaccount.NewCreateInstruction(
		solana.MustPublicKeyFromBase58(treasury.Address()), // payer (rent)
		owner, // wallet the ATA belongs to
		c.usdcMint,
	).Build()

	sig, err := c.signSendConfirm(ctx, treasury, []solana.Instruction{ix})
	if err != nil {
		return ata.String(), sig, fmt.Errorf("creating USDC ATA: %w", err)
	}
	return ata.String(), sig, nil
}

func (c *SolanaClient) TransferUSDC(ctx context.Context, from crypto.Keypair, toATA string, microUSDC uint64) (string, error) {
	fromOwner := solana.MustPublicKeyFromBase58(from.Address())
	fromATA, _, err := solana.FindAssociatedTokenAddress(fromOwner, c.usdcMint)
	if err != nil {
		return "", err
	}
	toATApk, err := solana.PublicKeyFromBase58(toATA)
	if err != nil {
		return "", fmt.Errorf("invalid destination ATA %q: %w", toATA, err)
	}

	ix := token.NewTransferCheckedInstruction(
		microUSDC, usdcDecimals,
		fromATA, c.usdcMint, toATApk, fromOwner,
		nil, // no multisig signers
	).Build()

	return c.signSendConfirm(ctx, from, []solana.Instruction{ix})
}

func (c *SolanaClient) TransferSOL(ctx context.Context, from crypto.Keypair, toAddress string, lamports uint64) (string, error) {
	fromPub := solana.MustPublicKeyFromBase58(from.Address())
	toPub, err := solana.PublicKeyFromBase58(toAddress)
	if err != nil {
		return "", fmt.Errorf("invalid destination %q: %w", toAddress, err)
	}
	ix := system.NewTransferInstruction(lamports, fromPub, toPub).Build()
	return c.signSendConfirm(ctx, from, []solana.Instruction{ix})
}

// ConfirmTx polls signature status until the transaction is finalized, the tx
// is reported failed, or the context is cancelled.
func (c *SolanaClient) ConfirmTx(ctx context.Context, sigStr string) error {
	sig, err := solana.SignatureFromBase58(sigStr)
	if err != nil {
		return fmt.Errorf("invalid signature %q: %w", sigStr, err)
	}
	return confirmSignature(ctx, c.rpc, sig)
}

// confirmSignature blocks until a signature is finalized, reported failed, or
// the context is cancelled. Shared by the Client and the Jupiter swapper.
func confirmSignature(ctx context.Context, cl *rpc.Client, sig solana.Signature) error {
	for {
		res, err := cl.GetSignatureStatuses(ctx, true, sig)
		if err == nil && len(res.Value) > 0 && res.Value[0] != nil {
			st := res.Value[0]
			if st.Err != nil {
				return fmt.Errorf("transaction %s failed on-chain: %v", sig, st.Err)
			}
			// "Confirmed" (supermajority-voted) is enough for a devnet demo and
			// lands in ~2-5s; waiting for "finalized" (~20-30s on public devnet)
			// made confirming calls exceed the client timeout. Finalized still
			// counts as success (it's strictly stronger).
			if st.ConfirmationStatus == rpc.ConfirmationStatusConfirmed ||
				st.ConfirmationStatus == rpc.ConfirmationStatusFinalized {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(confirmPollInterval):
		}
	}
}

// signSendConfirm builds a single-signer transaction (signer == fee payer),
// signs it with the derived key, broadcasts it, and blocks until finalized.
func (c *SolanaClient) signSendConfirm(ctx context.Context, signer crypto.Keypair, ixs []solana.Instruction) (string, error) {
	// Fetch the blockhash at finalized commitment: it's guaranteed propagated
	// cluster-wide so the tx doesn't hit "BlockhashNotFound" at send time (a
	// confirmed-commitment blockhash can be too new for the leader), while still
	// being recent enough to remain valid. Confirmation, in contrast, only waits
	// for "confirmed" (see confirmSignature) so the call returns quickly.
	recent, err := c.rpc.GetLatestBlockhash(ctx, rpc.CommitmentFinalized)
	if err != nil {
		return "", fmt.Errorf("fetching blockhash: %w", err)
	}
	signerPub := solana.MustPublicKeyFromBase58(signer.Address())

	tx, err := solana.NewTransaction(ixs, recent.Value.Blockhash, solana.TransactionPayer(signerPub))
	if err != nil {
		return "", fmt.Errorf("building transaction: %w", err)
	}

	priv := solana.PrivateKey(signer.PrivateKeyBytes())
	if _, err := tx.Sign(func(key solana.PublicKey) *solana.PrivateKey {
		if key.Equals(signerPub) {
			return &priv
		}
		return nil
	}); err != nil {
		return "", fmt.Errorf("signing transaction: %w", err)
	}

	sig, err := c.rpc.SendTransaction(ctx, tx)
	if err != nil {
		return "", fmt.Errorf("broadcasting transaction: %w", err)
	}
	if err := c.ConfirmTx(ctx, sig.String()); err != nil {
		// The signature is returned even on confirm failure so the caller can
		// record/alert - the tx may have landed, this is not proof it didn't.
		return sig.String(), err
	}
	return sig.String(), nil
}

// accountExists reports whether an account is present on-chain, treating the
// RPC "not found" as a clean false rather than an error.
func (c *SolanaClient) accountExists(ctx context.Context, pk solana.PublicKey) (bool, error) {
	_, err := c.rpc.GetAccountInfo(ctx, pk)
	if err != nil {
		if errors.Is(err, rpc.ErrNotFound) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}
