# Handoff — ussycode

_Current as of 2026-03-20. Read this before touching any code._

---

## 1. What Has Been Built

Six development tracks are complete or partially complete. The repo has a real foundation — this is not skeleton code.

### Completed tracks (A, B, E, F, G)

| Track | What was delivered |
|-------|--------------------|
| **A — Core Hardening** | Module renamed exedevussy → ussycode. Config wired with flag > env > default precedence. ZFS StorageBackend interface + implementation. nftables FirewallManager interface + NftablesManager. Integration test stubs and benchmarks. |
| **B — Server Pool** | gRPC proto definition (hand-written Go stubs). Agent binary (cmd/ussyverse-agent). Ed25519 PKI with CA/intermediate/node certs + join tokens. Agent heartbeat with /proc metrics. Control plane node manager with health state machine. WireGuard interface + in-memory stub. Two-phase VM placement scheduler. Agent installer script. |
| **E — API & Admin** | POST /exec with usy0/usy1 token auth + per-fingerprint rate limiting. Admin web panel with magic-link login, session store, dashboard, user/VM/node pages. Trust levels (newbie/citizen/operator/admin) with per-tier VM/CPU/RAM/disk quotas enforced on VM creation. Custom domain support (share cname/cname-verify/cname-rm with DNS TXT verification + Caddy integration). |
| **F — Ussyverse** | BattleBussy Arena (matches + ELO + leaderboard). Agent templates (geoffrussy, battlebussy-agent, ragussy, swarmussy). Community command. Ussyverse branding in welcome message and help. |
| **G — Deployment** | Ansible playbooks with 6 roles (common/zfs/firecracker/caddy/ussycode/monitoring). Agent installer enhanced with checksum verification and distro detection. Control plane installer (~500 lines). Five comprehensive docs (getting-started, self-hosting, contributing-compute, architecture, api). |

### Partially complete (Track C — 1/5)

- **C.1 done:** Tutorial command with 10 lessons, resume/jump/reset, interactive command validation, DB-backed progress (migration 002). 5 tests pass.
- **C.2–C.5 not done:** Browser command skeleton exists (`internal/ssh/browser.go`) but the auth URL it generates is broken (see blockers below). Doc command, project templates, and MOTD are not started.

### Test counts

- **80+ tests** across **12 packages with test files**
- `internal/api` — 18 tests (POST /exec, auth, rate limiting, error cases)
- `internal/admin` — 27 tests (session store, auth middleware, all page handlers, template helpers)
- `internal/db` — passes (quota: 7 tests, custom domain: 8 tests, tutorial: included in ssh suite)
- `internal/ssh` — passes (tutorial, arena, gateway, community, commands)
- `internal/pki` — 7 tests (CA generation, cert issuance, join token lifecycle)
- `internal/scheduler` — 10 tests (placement, filtering, scoring, 100-node stress)
- `internal/storage` — 14 tests + benchmarks (ZFS mock runner)
- `internal/vm` — 10 tests + integration stubs + benchmarks (nftables mock)
- `internal/gateway` — **FAIL** (8/9 tests pass; 1 failure — see blockers)
- `internal/config`, `internal/auth` — pass
- `internal/proxy` — **[no test files]** — zero coverage on critical auth path
- `internal/agent`, `internal/controlplane`, `internal/mesh` — no test files

### Build

- `go build ./...` — clean
- `go.mod` — `go 1.24.0`, toolchain `go1.24.4`

---

## 2. What Is Broken Right Now (Phase 0 Blockers)

These are runtime bugs and structural gaps that must be fixed before any parity feature work. Do not build new features on top of these.

---

### Blocker 1 — API handler has three nil arguments at startup

**File:** `cmd/ussycode/main.go:142`

```go
apiHandler := api.NewHandler(database, nil, nil, logger.With("component", "api"), nil)
//                                       ^    ^                                     ^
//                                  executor  KeyResolver                        Config
```

**Impact:**
- Any call to `POST /exec` will nil-pointer panic — the executor is called directly with no nil guard
- `usy0.*` stateless token verification cannot resolve SSH public keys — KeyResolver is nil
- Rate limiting is not configured — Config is nil

**Fix:** wire a real command executor (shared with the SSH shell), a real KeyResolver backed by the DB, and pass `cfg` as the Config argument.

---

### Blocker 2 — Browser magic-link URL has no matching handler

**File:** `internal/ssh/browser.go:38`

```go
url := fmt.Sprintf("https://%s/__auth/magic/%s", s.gw.domain, token)
```

**Impact:**
- The URL emitted to the user (`/__auth/magic/<token>`) has no HTTP handler registered anywhere
- `internal/admin/admin.go` expects `/admin/login/callback?token=...`
- The browser login flow is end-to-end broken — the user gets a dead link

**Fix:** either register a handler at `/__auth/magic/{token}` that validates the token and redirects, or change `browser.go` to emit the `/admin/login/callback?token=...` URL. Also decide whether admin login and general browser login share one token type and one path.

---

### Blocker 3 — TestSMTPServer_DotStuffing fails

**File:** `internal/gateway/email_test.go:395`

```
email_test.go:420: open /tmp/TestSMTPServer_DotStuffing.../001/dotvm/home/ussycode/Maildir/new: no such file or directory
```

**Impact:**
- `go test ./...` fails
- Inbound email delivery reliability is unverified

**Fix:** the test creates the temp dir but does not `mkdir -p` the Maildir structure before running the delivery. Add `os.MkdirAll(filepath.Join(tmpDir, "home/ussycode/Maildir/new"), 0755)` in the test setup.

---

### Blocker 4 — Zero telemetry exists

**Files:** all of them

**Impact:**
- Per project requirements (AGENTS.md), telemetry is a day-zero requirement
- No OTEL wiring, no Maple Ingest connection, no structured metrics anywhere
- Cannot answer "did this request succeed, how long did it take, for which user"

**Fix:** add OTEL/Maple Ingest wiring (`localhost:3474`, OTLP HTTP). Instrument as a minimum: `POST /exec` request count + latency + auth failure reason, browser token creation/redemption, and SMTP delivery success/failure. See `PLAN-exe-dev-parity-roadmap.md §7` for the full telemetry plan.

---

### Blocker 5 — internal/proxy has zero test coverage

**Files:** `internal/proxy/auth.go`, `internal/proxy/caddy.go`

**Impact:**
- The auth proxy is the critical enforcement layer for all web access — who can see what
- It has never been tested
- Regressions in this package are invisible

**Fix:** add tests covering at minimum: owner access allowed, public VM access, share-by-user access, denied access (401/403), and middleware chain behavior.

---

### Blocker 6 — audit_logs table does not exist

**Files:** `internal/db/migrations/` (no audit migration), `internal/admin/admin.go`, `internal/db/queries.go`

**Impact:**
- Trust level changes, share mutations, and admin operations produce no audit trail
- The parity roadmap requires audit logs for all share mutations
- No migration creates this table; no Go code references it

**Fix:** add a new migration creating `audit_logs (id, actor_id, action, target_type, target_id, detail, created_at)`. Wire it into admin trust-level changes as the minimum first write.

---

### Blocker 7 — Proxy has no share-link redemption flow

**Files:** `internal/proxy/auth.go`

**Impact:**
- `ShareByLinkToken` exists in `internal/db/queries.go` — links can be created
- `internal/proxy/auth.go` never calls `ShareByLinkToken` — links can never be redeemed
- A user who receives a share link gets a 401/403, not a session

**Fix:** add a redemption handler to the proxy auth layer. When a request carries a valid link token (query param or cookie), call `ShareByLinkToken`, grant a short-lived session, and set a session cookie.

---

## 3. What To Do Next

Execute Phase 0 in this order. Each item should be a self-contained commit.

1. **Fix TestSMTPServer_DotStuffing** (smallest, good warm-up — restore green baseline)
2. **Wire API handler** (executor + KeyResolver + Config — all three, not just executor)
3. **Fix browser magic-link URL** (decide on one canonical browser-auth path, implement it end-to-end)
4. **Add proxy share-link redemption** (DB query already exists, just needs the proxy handler)
5. **Add proxy auth tests** (write tests for the paths that now exist)
6. **Create audit_logs migration and wire first writes** (migration + admin trust-level hook)
7. **Add telemetry foundation** (OTEL/Maple Ingest wiring + instrument /exec as first path)
8. **Verify green baseline** — `go test ./...` must pass with zero failures before Phase 1

Do not start Phase 1 (command surface normalization, provisioning polish, HTTPS loop) until all eight items above are done.

---

## 4. Key Files to Read

| File | Why |
|------|-----|
| `PLAN-exe-dev-parity-roadmap.md` | Full roadmap with all phases, gap analysis, and product context — read this before any feature work |
| `spec.md` | Product specification (891 lines) |
| `docs/architecture.md` | ASCII diagrams: system overview, SSH flow, VM lifecycle, networking, storage, multi-node |
| `cmd/ussycode/main.go` | Entry point — API wiring bug is at line 142 |
| `internal/ssh/browser.go` | Browser command — URL mismatch is at line 38 |
| `internal/proxy/auth.go` | Auth proxy — no tests, no share-link redemption |
| `internal/gateway/email_test.go` | DotStuffing failure is at line 395 |
| `internal/api/handler.go` | API handler — shows what nil executor/KeyResolver/Config breaks |
| `PROGRESS-{A,B,C,E,F,G}.md` | Per-track progress files with detailed implementation notes |

---

## 5. Known False Positives / Clarifications

- **AUDIT-REPORT.md errors:** A previous session's AUDIT-REPORT.md contained errors (reported things as broken that were not broken, or referenced wrong file/line numbers). The PROGRESS files are the verified source of truth for what was actually implemented.

- **LSP / IDE errors:** The pre-existing `internal/admin/embed.go` issue (empty `web/templates` dir at compile time under some tooling) does not affect `go build ./...` or `go test ./...`. Build output is the source of truth, not IDE warnings.

- **internal/proxy "no test files":** This is not a false positive — it genuinely has zero tests. It's a real gap listed as Blocker 5 above.

- **Track D:** Was merged into Track E. No Track D progress file exists. This is expected.

- **WireGuard stub:** `internal/mesh/wireguard.go` is intentionally a stub (in-memory, no real WireGuard). Production implementation needs `tailscale` imports for magicsock/DERP. The interface contract is ready — only the implementation needs to be swapped when that becomes a priority (Phase 7).

- **gRPC proto:** `proto/node/v1/node.proto` is documentation. The Go stubs in `internal/proto/nodev1/` are hand-written. When `protoc` tooling is set up, replace the hand-written stubs with generated code.
