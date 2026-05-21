# SETUP_WALLET.md — Solana Wallet Setup for C2 Blockchain Memo

This guide walks you through EVERYTHING needed to get Solana wallets set up for the C2
blockchain memo system. **Zero crypto knowledge assumed.** No real money needed.

---

## Table of Contents

- [1. Install Solana CLI Tools](#1-install-solana-cli-tools)
- [2. Generate a Wallet (Keypair)](#2-generate-a-wallet-keypair)
- [3. Get Free Devnet SOL (Faucet)](#3-get-free-devnet-sol-faucet)
- [4. Check Your Balance](#4-check-your-balance)
- [5. Find Your Wallet Address](#5-find-your-wallet-address)
- [6. Generate a Wallet Without Solana CLI (Pure OpenSSL)](#6-generate-a-wallet-without-solana-cli-pure-openssl)
- [7. Wallet File Format Explained](#7-wallet-file-format-explained)
- [8. Security Notes](#8-security-notes)

---

## 1. Install Solana CLI Tools

The Solana CLI is needed for keypair generation and faucet access. It's one command:

```bash
sh -c "$(curl -sSfL https://release.anza.xyz/stable)"
```

This installs the `solana` command (maintained by Anza, the core Solana dev team).

**Verify installation:**

```bash
solana --version
```

Expected output: `solana-cli 2.x.x`

> **What about the old Solana Labs CLI?** The old `solana-cli` from Solana Labs is being
> replaced by the Anza distribution. Both work. The command above gets you the current
> standard one.

---

## 2. Generate a Wallet (Keypair)

A Solana wallet is just a **random 64-byte private key** + a **32-byte public key** derived
from it. The keypair file is what the server and client use to sign.

**Generate a keypair:**

```bash
solana-keygen new --outfile ~/.config/solota/operator.json
```

You'll be prompted for a passphrase. **You can just press Enter for no passphrase** (fine
for testing).

This creates a JSON file containing the raw bytes of your keypair as a JSON integer array:

```json
[12, 34, 56, 78, 91, ... 64 numbers total ...]
```

**Generate a second keypair for the implant (different wallet):**

```bash
solana-keygen new --outfile ~/.config/solota/implant.json
```

> **Protip:** Store both keypairs somewhere safe. The private key is the WHOLE file.
> Anyone with this file controls the wallet.

---

## 3. Get Free Devnet SOL (Faucet)

You need a tiny amount of SOL to pay transaction fees. On **devnet** (test network), it's
completely free.

### Method A: Solana CLI Airdrop (easiest)

```bash
# Switch to devnet
solana config set --url devnet

# Get 2 free SOL
solana airdrop 2
```

**If that fails** with "transaction unavailable" or similar (faucets rate-limit), try:

```bash
# Smaller amounts often work
solana airdrop 1
solana airdrop 1
solana airdrop 0.5
```

### Method B: Web Faucets

If the CLI faucet doesn't work, use a web faucet:

1. **Solana Devnet Faucet (official):**
   - Open: <https://faucet.solana.com/>
   - Select **Devnet**
   - Paste your wallet address (see [§5](#5-find-your-wallet-address))
   - Click "Confirm Airdrop"

2. **QuickNode Faucet (backup):**
   - <https://faucet.quicknode.com/solana/devnet>

3. **Sol Faucet (backup):**
   - <https://solfaucet.com/>

### How much SOL do you need?

- Each memo transaction costs ~**0.000005 SOL** (half a cent on mainnet, free on devnet)
- With 1 SOL you can send **200,000 commands**
- 2 SOL from a single airdrop is basically infinite for testing

### Devnet vs Mainnet Costs

| Network | Cost per memo tx | Source of SOL |
|---------|------------------|---------------|
| devnet  | Free (faucet)    | Free faucet   |
| testnet | Free (faucet)    | Free faucet   |
| mainnet | ~0.000005 SOL (~$0.001) | Buy from exchange |

**For testing, use devnet. You don't need real money.**

---

## 4. Check Your Balance

```bash
solana balance --url devnet
```

Should show something like `2 SOL` after the airdrop.

---

## 5. Find Your Wallet Address

```bash
solana-keygen pubkey ~/.config/solota/operator.json
```

This prints the **base58 address** (starts with a number or letter, ~44 characters).
Example: `7q6MgewGQzr3JwjJ8m7TzLfhTQAQScoXCaxzeNy9btRz`

You can also get it from the keypair file directly:

```bash
cat ~/.config/solota/operator.json | solana-keygen pubkey
```

**This address is PUBLIC.** It's safe to share — it's how people send you SOL and how
the C2 implant identifies which transactions to watch.

---

## 6. Generate a Wallet Without Solana CLI (Pure OpenSSL)

If you can't or won't install the Solana CLI, you can generate a wallet using just OpenSSL
and base58 encoding. This requires the base58 tool (`pip install base58` or use Python).

```bash
# 1. Generate 32 random bytes (the private key seed)
openssl rand -hex 32 > private-key.hex

# 2. Convert to raw bytes
xxd -r -p private-key.hex > private-key.bin

# 3. Create the Solana keypair JSON file (64 bytes: seed + derived pubkey)
#    We need a Python one-liner for the Ed25519 key derivation:
python3 -c "
import json
from hashlib import sha512

# Read the seed (32 bytes)
with open('private-key.hex') as f:
    seed = bytes.fromhex(f.read().strip())

# Ed25519 key expansion (simplified — in production use ed25519 lib)
# For the Solana CLI format, we need the full 64-byte keypair.
# The simplest approach: just install solana CLI for keygen.
# OR use Python's ed25519:
import nacl.bindings as nb

seed_bytes = seed
pk = nb.crypto_sign_seed_keypair(seed_bytes)[0]
keypair = list(seed_bytes + pk)

with open('operator.json', 'w') as f:
    json.dump(keypair, f)
print('operator.json created')
"
```

> **Honest advice:** Just install the Solana CLI. It's one command (`curl ... | sh`),
> it handles key derivation correctly, and the keypair file format is exactly what this
> C2 system expects. The OpenSSL method is shown here for understanding, not because
> it's easier.

---

## 7. Wallet File Format Explained

The Solana CLI creates keypair files in this format:

```json
[157,75,198,234,182,43,173,167,208,19,22,127,239,230,14,99,44,135,102,226,237,142,39,156,72,86,169,196,139,161,244,15,33,157,174,179,215,156,10,3,126,196,247,70,16,106,99,210,212,203,227,170,11,111,209,62,39,154,230,143,147,50,77,174]
```

- **Bytes 0–31** (first 32): The **private key seed** (keep secret!)
- **Bytes 32–63** (last 32): The **public key** (derived from the seed)

Both the `--keypair` flag in the server and the `--keypair` flag in the client accept
this JSON array format AND the base58-encoded private key format.

---

## 8. Security Notes

- **The keypair file IS the private key.** Protect it like a password.
- **For production C2:** Use a dedicated wallet with only enough SOL for a few hundred
  transactions. Top it up periodically.
- **For devnet testing:** Never use a mainnet wallet. Devnet SOL is free and infinite.
- **The chain is public.** Everyone can see the memo text. Don't send credentials or
  secrets as commands (or encrypt them first — see README for encryption notes).
- **The implant wallet address is public** by design — anyone can send it transactions.
  The C2 relies on the fact that only the operator knows which address is an implant.

---

## Quick Start (TL;DR)

```bash
# 1. Install Solana CLI
sh -c "$(curl -sSfL https://release.anza.xyz/stable)"

# 2. Generate operator wallet
solana-keygen new --outfile ~/.config/solota/operator.json

# 3. Generate implant wallet
solana-keygen new --outfile ~/.config/solota/implant.json

# 4. Get free devnet SOL
solana config set --url devnet
solana airdrop 2

# 5. Note the addresses
echo "Operator: $(solana-keygen pubkey ~/.config/solota/operator.json)"
echo "Implant:  $(solana-keygen pubkey ~/.config/solota/implant.json)"

# 6. Start the implant (watches its own address)
./bin/client --keypair ~/.config/solota/implant.json --rpc devnet

# 7. In another terminal, send a command
./bin/server --keypair ~/.config/solota/operator.json --rpc devnet
> send <IMPLANT_ADDRESS> whoami
```
