// wallet-backend is the custodial crypto edge (Phase 4): it provisions a Solana
// deposit address per account, watches the chain for incoming USDC/SOL, and
// credits/debits the real balance ledger in bet-backend through the atomic
// /users/:id/adjust endpoint. It stores custody state only (address<->account,
// deposit/withdrawal ledgers) - never balances. Everything network-touching and
// the ledger are behind interfaces so the money logic is unit-tested with fakes.
package main

import (
	"context"
	"log"
	"time"

	"github.com/gin-gonic/gin"

	"tchebet/wallet-backend/betclient"
	"tchebet/wallet-backend/chain"
	"tchebet/wallet-backend/config"
	"tchebet/wallet-backend/crypto"
	"tchebet/wallet-backend/db"
	"tchebet/wallet-backend/handlers"
	"tchebet/wallet-backend/poller"
	"tchebet/wallet-backend/service"
	"tchebet/wallet-backend/store"
)

func main() {
	log.Println("Starting Tchebet Wallet Backend...")

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	database, err := db.InitDB()
	if err != nil {
		log.Fatalf("database: %v", err)
	}
	st := store.New(database)

	master, err := crypto.NewMasterWalletFromMnemonic(cfg.MasterMnemonic, "")
	if err != nil {
		log.Fatalf("master wallet: %v", err)
	}

	// Real, network-touching adapters (Solana RPC + Jupiter). The service and
	// poller only ever see the chain.Client/Swapper/DepositScanner interfaces.
	solClient, err := chain.NewSolanaClient(cfg.SolanaRPCURL, cfg.USDCMint)
	if err != nil {
		log.Fatalf("solana client: %v", err)
	}
	scanner, err := chain.NewSolanaScanner(cfg.SolanaRPCURL, cfg.USDCMint)
	if err != nil {
		log.Fatalf("solana scanner: %v", err)
	}
	swapper := chain.NewJupiterSwapper(cfg.SolanaRPCURL, cfg.USDCMint)

	ledger := betclient.New(cfg.BetBackendURL)

	svc := service.New(master, st, ledger, solClient, swapper, service.Limits{
		Min:              cfg.WithdrawalMinMicroUSDC,
		Max:              cfg.WithdrawalMaxMicroUSDC,
		ManualReviewOver: cfg.WithdrawalManualReviewOver,
	}, cfg.BRLPerUSDMicros, cfg.FaucetMicroUSDC)

	// Background deposit watcher.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dp := poller.New(st, scanner, svc, time.Duration(cfg.DepositPollSeconds)*time.Second)
	go dp.Run(ctx)

	r := gin.Default()
	handlers.RegisterRoutes(r, svc, st)

	log.Printf("Tchebet Wallet Backend running on port %s...", cfg.Port)
	if err := r.Run(":" + cfg.Port); err != nil {
		log.Fatalf("server failed to run: %v", err)
	}
}
