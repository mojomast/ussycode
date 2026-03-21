# Handoff — ussycode

_Current as of 2026-03-21. Read this before touching any code._

## Completed: SSH Hardening (hardenussy branch)

### What was done

Three security hardening changes to the SSH package:

1. **Locked down `publicKeyHandler`** in `internal/ssh/gateway.go`:
   - Removed the Tailscale IP (100.64.0.0/10) bypass that allowed all keys from Tailscale IPs
   - Now ALL unknown users must be verified against routussy, regardless of source IP
   - If `RoutussyURL` is not configured, unknown users are rejected entirely (no more open registration)
   - Known local-DB users are still always allowed
   - Removed the now-unused `isTailscaleIP()` function

2. **Disabled interactive registration** in `sessionHandler()` in `internal/ssh/gateway.go`:
   - Replaced `handleRegistration()` call with a rejection message
   - Directs users to register at `https://discord.gg/ussyverse`

3. **Per-user SSH keys for VM proxy** in `internal/ssh/commands.go`:
   - Changed `proxySSHSession()` signature to accept `userID int64`
   - Replaced shared `hostKeyPath` with per-user key via `VM.EnsureUserKey(userID)`
   - Updated caller `cmdSSH()` to pass `s.user.ID`

### Files Modified

1. **`internal/ssh/gateway.go`** — publicKeyHandler hardening + registration removal
2. **`internal/ssh/commands.go`** — proxySSHSession per-user key + caller update

### Previously completed on this branch (by earlier Ralph)

- **`internal/vm/manager.go`** — `EnsureUserKey()`, `UserKeyPath()`, `UserPublicKey()` methods + keys directory creation
- **`internal/ssh/shell.go`** — `vmSSHKeys()` updated to inject per-user gateway public key instead of shared host key

### Context for Next Task

- All LSP errors in the repo are false positives from missing `go mod download`
- The `handleRegistration` function still exists somewhere but is no longer called from `sessionHandler` — it could be cleaned up
- Phase 0 blockers from devplan.md still need resolution
- `go build ./...` should remain clean once `go mod download` is run

---

## Previous Context (preserved from earlier handoff)

- 80+ tests across 12 packages
- 1 known test failure: `TestSMTPServer_DotStuffing` in `internal/gateway`
- Phase 0 blockers (P0-1 through P0-8) still need resolution before feature work
- `go build ./...` should remain clean
