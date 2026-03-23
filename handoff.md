# Handoff — ussycode

_Current as of 2026-03-21. Read this before touching any code._

## Current deployment checkpoint (2026-03-23)

The shuv.dev dev environment (`ussycode.shuv.dev`, `apiussy.shuv.dev`, `*.ussycode.shuv.dev`) has now been brought up on a fresh GCP host and aligned much more closely with the intended Ansible/self-hosting topology.

### Live host facts
- Host: `ussycode-dev-01`
- Zone: `us-west4-b`
- Public IP: `34.186.6.68`
- ZFS disk by-id: `/dev/disk/by-id/google-persistent-disk-1`

### Confirmed live ports/listeners
- nginx public edge: `:80`, `:443`
- internal Caddy: `127.0.0.1:8085`
- Caddy admin: `127.0.0.1:2019`
- ussycode API: `127.0.0.1:8080`
- ussycode admin: `127.0.0.1:9090`
- auth proxy: `127.0.0.1:9876`
- metadata: `:8083`
- SSH gateway: `:2224`

### Confirmed public behavior
- `http://ussycode.shuv.dev/` redirects to HTTPS
- `https://ussycode.shuv.dev/admin/` redirects to `/admin/login`
- `https://apiussy.shuv.dev/version` returns version JSON

### Important deployment lesson
The Ansible `ussycode` role currently clones/builds from upstream GitHub (`mojomast/ussycode` `main`) rather than this local patched branch. That caused the remote host to briefly run an older runtime that tried to make Caddy own public TLS again.

To recover, a local binary built from this workspace branch (`hardenussy-shuvdev`) was manually copied to the host and installed at `/usr/local/bin/ussycode`.

### Additional runtime fix applied locally
`internal/proxy/caddy.go` was further updated so the runtime-loaded Caddy JSON now explicitly includes:
- `automatic_https.disable=true`
- `automatic_https.disable_redirects=true`
- fixed internal listener `127.0.0.1:8085`
- explicit admin listener `localhost:2019`
- internal API/admin host routes
- `/healthz`

This eliminated the previous runtime attempts to bind `127.0.0.1:80`.

### Follow-up completed
The Ansible role has now been taught to deploy the intended local branch/artifact instead of blindly cloning upstream `main`.

Current behavior:
- `roles/ussycode` supports a local-source deploy path via:
  - `ussycode_local_repo_path`
  - `ussycode_local_repo_ref`
- the local tree is archived on the control machine, copied to the target, extracted into `/opt/ussycode-src`, and then built on-host
- shuv.dev inventory now points at the local workspace repo/branch:
  - `/home/shuv/repos/ussyverse/ussyco.de/ussycode`
  - `hardenussy-shuvdev`
- validated by rerunning the `ussycode` Ansible tag successfully
- deployed source provenance is written on-host to:
  - `/opt/ussycode-src/.deployed-source.txt`

### Routussy / shuv.dev checkpoint
Option A is now in place:
- `apiussy.shuv.dev` remains the ussycode API host
- Routussy now has its own shuv.dev host:
  - `https://routussy.shuv.dev`

What was changed:
- added DNS A record for `routussy.shuv.dev` -> `34.186.6.68`
- expanded the nginx/certbot SAN cert to include `routussy.shuv.dev`
- updated nginx edge routing so `routussy.shuv.dev` proxies to local Routussy on `127.0.0.1:3000`
- deployed Routussy on-host as a `systemd --user` service for user `shuv`
- configured ussycode with:
  - `USSYCODE_ROUTUSSY_URL=https://routussy.shuv.dev`
  - matching `USSYCODE_ROUTUSSY_INTERNAL_KEY`

Validated:
- `https://routussy.shuv.dev/health` returns `200`
- `/ussycode/*` endpoints require bearer auth (`401` without token)
- authenticated `/ussycode/user-by-fingerprint` returns `404` for unknown fingerprints, which is expected
- Discord `/ussycode-config` instructions are now env-driven for host/port/domain instead of hardcoded to `dev.ussyco.de`

Relevant env vars now used by Routussy commands:
- `USSYCODE_GATEWAY_HOST`
- `USSYCODE_GATEWAY_PORT`
- `USSYCODE_VM_BASE_DOMAIN`

shuv.dev values:
- `USSYCODE_GATEWAY_HOST=ussycode.shuv.dev`
- `USSYCODE_GATEWAY_PORT=2224`
- `USSYCODE_VM_BASE_DOMAIN=ussycode.shuv.dev`

Important remaining blocker for full end-to-end auth testing:
- the test SSH fingerprint must first exist in Routussy's approved ussycode key set via the shuv Discord-backed approval flow
- until a key is approved there, ussycode SSH auth will correctly reject it even though routing/integration is wired

### Still worth cleaning up later
- `caddy.service` status text may still show an old failed reload message in `systemctl status`, even though the currently loaded config is correct and active via the admin API.
- SSH auth on `:2224` now reflects the hardening branch behavior: unknown keys are rejected unless the user/key exists in DB or the fingerprint is approved in Routussy.
- Routussy deployment is currently ad hoc on-host (systemd user service), not yet codified in Ansible.

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
