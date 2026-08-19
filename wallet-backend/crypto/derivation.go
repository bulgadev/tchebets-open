// Package crypto owns wallet-backend's crown-jewels: turning one BIP39 master
// mnemonic into the per-account Solana keypairs the platform custodies.
//
// The derivation is SLIP-0010 over ed25519 (the curve Solana uses), on the
// standard Solana account path m/44'/501'/index'/0' - the same path Phantom
// and Solflare derive, so an operator could in principle import the master
// mnemonic into one of those wallets and see the same addresses. ed25519 only
// supports HARDENED derivation, so every path level here is hardened.
//
// This is implemented in-house (rather than pulling a whole second Solana SDK
// just for HD derivation) precisely because it's the most security-critical
// code in the service: it's ~50 lines of well-specified crypto, and it is
// pinned against the canonical SLIP-0010 ed25519 test vectors in
// derivation_test.go. Never let this drift untested.
package crypto

import (
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/sha512"
	"encoding/binary"
	"fmt"

	"github.com/mr-tron/base58"
	"github.com/tyler-smith/go-bip39"
)

// hardenedOffset is 2^31: BIP32/SLIP-0010 mark a child index as "hardened" by
// setting its top bit. ed25519 derivation is hardened-only, so every index we
// derive is OR'd with this.
const hardenedOffset uint32 = 0x80000000

// ed25519SeedKey is the fixed HMAC key SLIP-0010 uses to turn the BIP39 seed
// into the ed25519 master node.
var ed25519SeedKey = []byte("ed25519 seed")

// Keypair is a single derived Solana keypair. The private key is the 64-byte
// ed25519 form (32-byte seed || 32-byte public key) that Solana tooling
// expects; Address() is its base58 public key, i.e. the on-chain address.
type Keypair struct {
	priv ed25519.PrivateKey
}

// PublicKey returns the raw 32-byte ed25519 public key.
func (k Keypair) PublicKey() ed25519.PublicKey {
	return k.priv.Public().(ed25519.PublicKey)
}

// Address returns the base58-encoded public key - the Solana address funds are
// sent to.
func (k Keypair) Address() string {
	return base58.Encode(k.PublicKey())
}

// PrivateKeyBytes returns the 64-byte ed25519 private key (seed || pubkey).
// This is the value that signs transactions; treat it as a secret and never
// log it.
func (k Keypair) PrivateKeyBytes() []byte {
	return append([]byte(nil), k.priv...)
}

// PrivateKeyBase58 returns the 64-byte private key base58-encoded - the format
// solana-keygen / Phantom "import private key" expect, for export/recovery.
func (k Keypair) PrivateKeyBase58() string {
	return base58.Encode(k.priv)
}

// MasterWallet holds the BIP39 master seed and derives per-account keypairs
// from it. Construct it once at startup from the master mnemonic and keep it
// off the heap boundaries you can (never serialize it, never log it).
type MasterWallet struct {
	seed []byte // 64-byte BIP39 seed
}

// NewMasterWalletFromMnemonic validates the mnemonic and derives the BIP39
// seed. passphrase is the optional BIP39 "25th word" - empty string for the
// common case.
func NewMasterWalletFromMnemonic(mnemonic, passphrase string) (*MasterWallet, error) {
	if !bip39.IsMnemonicValid(mnemonic) {
		return nil, fmt.Errorf("invalid BIP39 mnemonic")
	}
	seed := bip39.NewSeed(mnemonic, passphrase)
	return &MasterWallet{seed: seed}, nil
}

// GenerateMnemonic returns a fresh 24-word (256-bit entropy) BIP39 mnemonic -
// used once to create the platform master wallet.
func GenerateMnemonic() (string, error) {
	entropy, err := bip39.NewEntropy(256)
	if err != nil {
		return "", fmt.Errorf("generating entropy: %w", err)
	}
	return bip39.NewMnemonic(entropy)
}

// DeriveAccount returns the keypair at m/44'/501'/index'/0'. index is the
// platform account's stable derivation index - each account gets a distinct
// one so a deposit into an address unambiguously credits one account.
func (w *MasterWallet) DeriveAccount(index uint32) Keypair {
	path := []uint32{44, 501, index, 0}
	seed32 := w.derivePath(path)
	return Keypair{priv: ed25519.NewKeyFromSeed(seed32)}
}

// derivePath walks the SLIP-0010 ed25519 chain from the master node down each
// (hardened) index in path, returning the final 32-byte key that seeds the
// ed25519 keypair. path elements are given un-hardened (44, 501, ...) and are
// hardened here.
func (w *MasterWallet) derivePath(path []uint32) []byte {
	key, chainCode := masterKey(w.seed)
	for _, index := range path {
		key, chainCode = deriveChildHardened(key, chainCode, index|hardenedOffset)
	}
	return key
}

// masterKey is SLIP-0010's master-node generation: HMAC-SHA512("ed25519 seed",
// seed) split into IL (key) and IR (chain code).
func masterKey(seed []byte) (key, chainCode []byte) {
	sum := hmacSHA512(ed25519SeedKey, seed)
	return sum[:32], sum[32:]
}

// deriveChildHardened is SLIP-0010's hardened CKDpriv for ed25519:
// I = HMAC-SHA512(chainCode, 0x00 || key || ser32BE(index)), split into the
// child key (IL) and child chain code (IR). index must already carry the
// hardened bit.
func deriveChildHardened(key, chainCode []byte, index uint32) (childKey, childChainCode []byte) {
	data := make([]byte, 1+32+4)
	data[0] = 0x00
	copy(data[1:33], key)
	binary.BigEndian.PutUint32(data[33:], index)

	sum := hmacSHA512(chainCode, data)
	return sum[:32], sum[32:]
}

func hmacSHA512(key, data []byte) []byte {
	mac := hmac.New(sha512.New, key)
	mac.Write(data)
	return mac.Sum(nil)
}
