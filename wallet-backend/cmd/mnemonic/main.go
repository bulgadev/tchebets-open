// Command mnemonic prints a fresh 24-word BIP39 master mnemonic to stdout. Run
// it once to generate the platform master seed, then set the value as the
// wallet-backend WALLET_MNEMONIC env var. The service refuses to start without
// one (config.Load fails closed).
//
//	go run ./cmd/mnemonic
//
// SECURITY: this is the crown jewel. On devnet a plain env var is acceptable;
// mainnet must move signing behind a KMS/HSM. Never commit or log the output.
package main

import (
	"fmt"
	"os"

	"tchebet/wallet-backend/crypto"
)

func main() {
	m, err := crypto.GenerateMnemonic()
	if err != nil {
		fmt.Fprintln(os.Stderr, "failed to generate mnemonic:", err)
		os.Exit(1)
	}
	fmt.Println(m)
}
