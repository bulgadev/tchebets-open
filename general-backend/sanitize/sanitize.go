// Package sanitize holds the field-level checks general-backend runs on every
// request before it ever gets forwarded to bet-backend. bet-backend won't take
// traffic straight from the frontend anymore, so these are the first (and for a
// few things like duplicate/oversized outcomes, the only) line of validation.
package sanitize

import (
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// MaxSanityInt caps fields like stake/balance/seed so a typo or overflow-ish
// value doesn't sail through as "technically positive". Not a real business
// limit, just a sanity backstop.
const MaxSanityInt = 1_000_000_000

// UUID trims and validates a UUID string, returning it normalized (lowercase).
func UUID(s string) (string, error) {
	trimmed := strings.TrimSpace(s)
	id, err := uuid.Parse(trimmed)
	if err != nil {
		return "", fmt.Errorf("invalid uuid: %s", s)
	}
	return id.String(), nil
}

// NonEmptyString trims s and rejects it if empty or longer than maxLen.
func NonEmptyString(s string, maxLen int) (string, error) {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return "", fmt.Errorf("value must not be empty")
	}
	if len(trimmed) > maxLen {
		return "", fmt.Errorf("value exceeds max length of %d", maxLen)
	}
	return trimmed, nil
}

// Outcomes trims every outcome, and rejects the set if there are too few,
// too many, any are empty/too long, or duplicates exist (case-insensitive).
func Outcomes(outcomes []string, minCount, maxCount, maxLen int) ([]string, error) {
	if len(outcomes) < minCount {
		return nil, fmt.Errorf("a market must have at least %d outcomes", minCount)
	}
	if len(outcomes) > maxCount {
		return nil, fmt.Errorf("a market must have at most %d outcomes", maxCount)
	}

	cleaned := make([]string, 0, len(outcomes))
	seen := make(map[string]bool, len(outcomes))
	for _, o := range outcomes {
		trimmed, err := NonEmptyString(o, maxLen)
		if err != nil {
			return nil, fmt.Errorf("invalid outcome %q: %w", o, err)
		}
		key := strings.ToLower(trimmed)
		if seen[key] {
			return nil, fmt.Errorf("duplicate outcome: %q", trimmed)
		}
		seen[key] = true
		cleaned = append(cleaned, trimmed)
	}

	return cleaned, nil
}

// base58Alphabet is Bitcoin/Solana's base58 set (no 0, O, I, l).
const base58Alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"

// SolanaAddress does a cheap defense-in-depth check on a withdrawal destination:
// base58 charset and the length range a 32-byte ed25519 pubkey encodes to
// (32-44 chars). It is not full validation - wallet-backend re-parses it with
// solana-go before ever signing - just a first gate against obvious junk.
func SolanaAddress(s string) (string, error) {
	trimmed := strings.TrimSpace(s)
	if len(trimmed) < 32 || len(trimmed) > 44 {
		return "", fmt.Errorf("invalid solana address length")
	}
	for _, r := range trimmed {
		if !strings.ContainsRune(base58Alphabet, r) {
			return "", fmt.Errorf("invalid solana address (non-base58 character)")
		}
	}
	return trimmed, nil
}

// PositiveInt64 rejects n <= 0 or n > max.
func PositiveInt64(n int64, max int64) (int64, error) {
	if n <= 0 {
		return 0, fmt.Errorf("value must be greater than 0")
	}
	if n > max {
		return 0, fmt.Errorf("value exceeds max of %d", max)
	}
	return n, nil
}

// NonNegativeInt64 rejects n < 0 or n > max.
func NonNegativeInt64(n int64, max int64) (int64, error) {
	if n < 0 {
		return 0, fmt.Errorf("value must not be negative")
	}
	if n > max {
		return 0, fmt.Errorf("value exceeds max of %d", max)
	}
	return n, nil
}
