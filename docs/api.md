# ussycode API Reference

The ussycode HTTPS API allows programmatic access to all platform features. It mirrors the SSH command interface but over HTTP.

## Base URL

```
https://ussyco.de
```

For self-hosted instances, replace with your domain.

## Authentication

All API requests require a Bearer token in the `Authorization` header:

```
Authorization: Bearer <token>
```

### Token Formats

ussycode supports two token formats:

#### Stateless Tokens (`usy0.`)

Format: `usy0.<base64url_permissions>.<base64url_ssh_signature>`

These tokens are:
- Self-contained (no database lookup needed)
- Signed with your SSH private key
- Time-limited (contain expiry timestamp)
- Optionally scoped to specific commands

**Permissions payload (JSON):**
```json
{
  "exp": 1735689600,
  "nbf": 1735603200,
  "cmds": ["ls", "new"],
  "ctx": ""
}
```

| Field | Type | Description |
|-------|------|-------------|
| `exp` | int64 | Expiry (Unix timestamp) |
| `nbf` | int64 | Not-before (Unix timestamp) |
| `cmds` | string[] | Allowed commands (empty = all) |
| `ctx` | string | Optional context string |

#### Database Tokens (`usy1.`)

Format: `usy1.<opaque_token_id>`

These tokens are:
- Stored in the database
- Revocable
- Created via the `admin` command

### Generating Tokens

Via the SSH shell:

```
> admin token create --ttl=24h --cmds=ls,new
usy0.eyJleHAiOjE3MzU2ODk2MDB9.c2lnbmF0dXJl...
```

Via the auth package (Go):

```go
import "github.com/mojomast/ussycode/internal/auth"

token, err := auth.SignToken(signer, "myhandle", 24*time.Hour, []string{"ls", "new"})
```

## Endpoints

### POST /exec

Execute an SSH command via HTTPS.

**Request:**

```json
{
  "command": "ls -l"
}
```

**Response (200):**

```json
{
  "output": "  NAME      STATUS   IMAGE     CPU  MEM   CREATED\n  myvm      running  ussyuntu  1    512M  2024-01-15\n",
  "exit_code": 0
}
```

**Example:**

```bash
curl -X POST https://ussyco.de/exec \
  -H "Authorization: Bearer usy0.eyJ..." \
  -H "Content-Type: application/json" \
  -d '{"command": "ls"}'
```

#### Creating a VM

```bash
curl -X POST https://ussyco.de/exec \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"command": "new --name=apivm --image=ussyuntu"}'
```

#### Listing VMs (JSON output)

```bash
curl -X POST https://ussyco.de/exec \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"command": "ls --json"}'
```

#### Stopping a VM

```bash
curl -X POST https://ussyco.de/exec \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"command": "stop myvm"}'
```

### GET /health

Health check endpoint. No authentication required.

**Response (200):**

```json
{
  "status": "ok",
  "time": "2024-01-15T10:30:00Z"
}
```

**Example:**

```bash
curl https://ussyco.de/health
```

### GET /version

Version information. No authentication required.

**Response (200):**

```json
{
  "version": "0.1.0",
  "go": "go1.25.7",
  "os": "linux",
  "arch": "amd64"
}
```

## Rate Limiting

The API applies per-fingerprint token bucket rate limiting:

| Parameter | Default |
|-----------|---------|
| Rate | 60 requests/minute |
| Burst | 10 requests |

When rate limited, the API returns:

**Response (429):**

```json
{
  "error": "rate limit exceeded",
  "code": 429
}
```

The `Retry-After` header indicates seconds until the next request is allowed:

```
Retry-After: 3
```

## Error Codes

| HTTP Code | Meaning | Example |
|-----------|---------|---------|
| 200 | Success | Command executed successfully |
| 400 | Bad Request | Empty command, malformed JSON |
| 401 | Unauthorized | Missing/invalid/expired token |
| 403 | Forbidden | Token doesn't permit this command |
| 404 | Not Found | Endpoint doesn't exist |
| 422 | Unprocessable Entity | Command failed (e.g., VM doesn't exist) |
| 429 | Too Many Requests | Rate limit exceeded |
| 504 | Gateway Timeout | Command timed out (30s limit) |

### Error Response Format

All errors return:

```json
{
  "error": "human-readable error message",
  "code": 401
}
```

## Available Commands

> **`--json` support:** Most list/query commands support `--json` for machine-readable output. Currently verified: `ls --json`, `ssh-key list --json`, `share list --json`.

All SSH shell commands are available via `POST /exec`:

| Command | Description |
|---------|-------------|
| `help` | Show help |
| `whoami` | Show user info |
| `ls` | List VMs |
| `ls -l` | List VMs (detailed) |
| `ls --json` | List VMs (JSON) |
| `new` | Create VM |
| `new --name=X --image=Y` | Create named VM with image |
| `rm <name>` | Delete VM |
| `start <name>` | Start VM |
| `stop <name>` | Stop VM |
| `restart <name>` | Restart VM |
| `rename <old> <new>` | Rename VM |
| `cp <name> [new]` | Clone VM |
| `tag <name> <tag>` | Add tag |
| `tag -d <name> <tag>` | Remove tag |
| `share url <name>` | Get share URL |
| `share collab <name> <user>` | Grant SSH access |
| `share pub <name>` | Make public |
| `share cname <name> <domain>` | Add custom domain |
| `ssh-key list` | List SSH keys |
| `ssh-key add <key>` | Add SSH key |

## Examples

### Full Workflow with curl

```bash
# Set your token
export TOKEN="usy0.eyJ..."

# Create a VM
curl -s -X POST https://ussyco.de/exec \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"command": "new --name=webapp"}' | jq .

# Check it's running
curl -s -X POST https://ussyco.de/exec \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"command": "ls --json"}' | jq .

# Get share URL
curl -s -X POST https://ussyco.de/exec \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"command": "share url webapp"}' | jq .

# Clean up
curl -s -X POST https://ussyco.de/exec \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"command": "rm webapp"}' | jq .
```

### Using with jq

```bash
# Get just VM names
curl -s -X POST https://ussyco.de/exec \
  -H "Authorization: Bearer $TOKEN" \
  -d '{"command": "ls --json"}' | jq -r '.output | fromjson | .[].name'
```

### Health Check Script

```bash
#!/bin/bash
status=$(curl -s https://ussyco.de/health | jq -r '.status')
if [ "$status" != "ok" ]; then
  echo "ussycode is down!"
  exit 1
fi
```
