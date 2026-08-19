// Command treasury prints the platform treasury/hot-wallet address - derivation
// index 0 of WALLET_MNEMONIC. This is the account that pays ATA rent and signs
// withdrawals, so it must be funded with (devnet) SOL before deposits/withdrawals
// work. Run it to learn the address to fund and monitor:
//
//	WALLET_MNEMONIC="..." go run ./cmd/treasury
package main

import (
	"fmt"
	"os"

	"tchebet/wallet-backend/crypto"
)

func main() {
	mnemonic := os.Getenv("WALLET_MNEMONIC")
	if mnemonic == "" {
		fmt.Fprintln(os.Stderr, "WALLET_MNEMONIC is required")
		os.Exit(1)
	}
	master, err := crypto.NewMasterWalletFromMnemonic(mnemonic, "")
	if err != nil {
		fmt.Fprintln(os.Stderr, "invalid mnemonic:", err)
		os.Exit(1)
	}
	// Index 0 is the reserved treasury index (service.treasuryIndex).
	fmt.Println(master.DeriveAccount(0).Address())
}
