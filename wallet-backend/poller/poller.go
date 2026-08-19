// Package poller is wallet-backend's background deposit watcher: the free,
// self-contained replacement for a paid webhook provider. On a fixed interval it
// walks every provisioned address, scans for new incoming transfers since that
// address's stored cursor, ingests each through the service (which credits the
// ledger idempotently), and advances the cursor. A lost cursor is safe - the
// signature-unique deposit ledger dedups, so a re-scan never double-credits.
package poller

import (
	"context"
	"log"
	"time"

	"tchebet/wallet-backend/chain"
	"tchebet/wallet-backend/service"
	"tchebet/wallet-backend/store"
)

type Poller struct {
	store    *store.Store
	scanner  chain.DepositScanner
	svc      *service.Service
	interval time.Duration
}

func New(st *store.Store, scanner chain.DepositScanner, svc *service.Service, interval time.Duration) *Poller {
	return &Poller{store: st, scanner: scanner, svc: svc, interval: interval}
}

// Run blocks, polling until the context is cancelled. Launch it in a goroutine.
func (p *Poller) Run(ctx context.Context) {
	log.Printf("deposit poller started (every %s)", p.interval)
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()

	for {
		if err := p.scanOnce(ctx); err != nil {
			log.Printf("deposit poll pass error: %v", err)
		}
		select {
		case <-ctx.Done():
			log.Println("deposit poller stopped")
			return
		case <-ticker.C:
		}
	}
}

// scanOnce walks every provisioned address once. One address's error is logged
// and skipped so a single bad account can't stall the whole poller.
func (p *Poller) scanOnce(ctx context.Context) error {
	addresses, err := p.store.ListAddresses(ctx)
	if err != nil {
		return err
	}
	for _, addr := range addresses {
		if err := p.scanAddress(ctx, addr.AccountID, addr.Address, addr.USDCATA); err != nil {
			log.Printf("scanning account %s (%s): %v", addr.AccountID, addr.Address, err)
		}
	}
	return nil
}

func (p *Poller) scanAddress(ctx context.Context, accountID, baseAddress, usdcATA string) error {
	// The base address is the cursor key: both SOL and USDC signatures are
	// scanned relative to it, and dedup on deposits.signature makes the shared
	// cursor safe.
	cursor, err := p.store.GetScanCursor(ctx, baseAddress)
	if err != nil {
		return err
	}

	found, newCursor, err := p.scanner.ScanDeposits(ctx, baseAddress, usdcATA, cursor)
	if err != nil {
		return err
	}

	for _, d := range found {
		if err := p.svc.IngestDeposit(ctx, accountID, d); err != nil {
			// Log and continue: the cursor is only advanced past deposits we've
			// at least attempted. A failed ingest stays re-scannable next pass.
			log.Printf("ingesting deposit %s for %s: %v", d.Signature, accountID, err)
			return nil
		}
	}

	if newCursor != "" && newCursor != cursor {
		if err := p.store.SetScanCursor(ctx, baseAddress, newCursor); err != nil {
			return err
		}
	}
	return nil
}
