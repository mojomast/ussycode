# ussycode Dev Plan — Current State

_Last updated: 2026-03-20. Source of truth for track status and next work._

---

## Track Completion Status

### Track A: Core Hardening ✅ COMPLETE
- [x] A.1: Rename exedevussy → ussycode (module path, imports, dirs, strings, image files, config)
- [x] A.2: Wire config.go into main.go (RegisterFlags, Validate, flag > env > default precedence)
- [x] A.3: ZFS Storage Backend (StorageBackend interface + ZFSBackend, 14 tests + benchmarks)
- [x] A.4: nftables Migration (FirewallManager interface + NftablesManager, 10 tests)
- [x] A.5: Enhanced Testing (integration stubs, benchmarks, pre-existing email import fix)

### Track B: Ussyverse Server Pool ✅ COMPLETE
- [x] B.1: gRPC Protocol Definition (proto/node/v1/node.proto + hand-written Go stubs)
- [x] B.2: Agent Binary (cmd/ussyverse-agent with join/run/status/version subcommands)
- [x] B.3: mTLS Certificate Management (Ed25519 CA + intermediates + node certs, 7 PKI tests)
- [x] B.4: Heartbeat & Health (agent-side metrics + control plane node manager + health state machine)
- [x] B.5: WireGuard Mesh (interface + in-memory stub + /10 subnet allocator)
- [x] B.6: VM Placement Scheduler (two-phase filter+score, 10 tests, 100-node stress test)
- [x] B.7: Agent Installer Script (deploy/install-agent.sh with OS detection + checksum verify)

### Track C: UX & Onboarding ⚠️ 1/5 DONE
- [x] C.1: Tutorial Command (10 lessons, resume/jump/reset, 5 tests, DB migration 002)
- [ ] C.2: Browser Command (browser.go skeleton exists but auth URL is broken — see Phase 0 blockers)
- [ ] C.3: Doc Command
- [ ] C.4: Project Templates
- [ ] C.5: Welcome & MOTD

### Track E: API & Admin ✅ COMPLETE
- [x] E.1: HTTPS API (POST /exec, GET /health, GET /version, usy0/usy1 tokens, rate limiting, 18 tests)
- [x] E.2: Admin Web Panel (sessions, magic-link auth, dashboard, users, VMs, nodes, 27 tests)
- [x] E.3: Trust Levels & Quotas (newbie/citizen/operator/admin, per-tier VM/CPU/RAM/disk limits, 7 tests)
- [x] E.4: Custom Domains (share cname/cname-verify/cname-rm, DNS TXT verification, Caddy integration, 8 tests)

### Track F: Ussyverse Integration ✅ COMPLETE
- [x] F.1: BattleBussy Arena (arena matches + ELO system + DB migration 007, tests pass)
- [x] F.2: Enhanced Agent Templates (geoffrussy, battlebussy-agent, ragussy, swarmussy)
- [x] F.3: Ussyverse Branding & Community (community command, welcome branding, help USSYVERSE section)

### Track G: Deployment & Ops ✅ COMPLETE
- [x] G.1: Ansible Playbooks (deploy/ansible/ — site.yml + ansible.cfg + 6 roles: common/zfs/firecracker/caddy/ussycode/monitoring)
- [x] G.2: Agent Installer Enhancement (checksum verification, distro detection, structured join summary)
- [x] G.3: Control Plane Installer (deploy/install-control.sh ~500 lines, full preflight + build + service setup)
- [x] G.4: Documentation (5 docs: getting-started, self-hosting, contributing-compute, architecture, api)

---

## Test Baseline

- **80+ tests across 12 packages with test files**
- **1 test failure:** `TestSMTPServer_DotStuffing` in `internal/gateway` (Maildir path issue)
- `internal/proxy` — [no test files] — zero coverage on critical auth path
- `internal/agent`, `internal/controlplane`, `internal/mesh` — no test files

---

## Phase 0 — Mandatory Blockers (fix before parity work)

_These must be resolved before building more features. Do not layer parity work on top of broken core flows._

- [ ] **P0-1: Wire API handler** — `cmd/ussycode/main.go:142` passes `nil` for executor, KeyResolver, AND Config; first real `/exec` call panics; rate limiting and `usy0.*` token verification are broken
- [ ] **P0-2: Fix browser magic-link URL** — `internal/ssh/browser.go:38` emits `/__auth/magic/<token>`; no HTTP handler exists for that path; admin expects `/admin/login/callback?token=...`; decide whether admin login and general browser login share one flow or two
- [ ] **P0-3: Fix TestSMTPServer_DotStuffing** — `internal/gateway/email_test.go:395` fails; test temp dir is created but Maildir subdirectory is not; fix test setup to create `home/ussycode/Maildir/new/` before delivering
- [ ] **P0-4: Add telemetry foundation** — zero OTEL/observability wiring exists; add Maple Ingest (`:3474` OTLP HTTP) wiring; instrument `/exec`, browser token create/redeem, and SMTP delivery as first three paths
- [ ] **P0-5: Add proxy auth tests** — `internal/proxy` has zero test files; add tests covering at minimum: owner access, public access, share-by-user access, denied access
- [ ] **P0-6: Create audit_logs table and wire it** — no migration creates the table; no Go code writes audit events; create migration + write to it from admin trust-level changes as a minimum
- [ ] **P0-7: Fix proxy share-link redemption** — `internal/proxy/auth.go` never calls `ShareByLinkToken`; share links can be created (DB query exists) but never redeemed; proxy has no flow to grant a session from a link token
- [ ] **P0-8: Restore green build/test baseline** — after the above, `go test ./...` must pass with zero failures

### Phase 0 Validation
- [ ] `curl -X POST /exec` works with a real executor, real KeyResolver, and Config
- [ ] `browser` command URL resolves to a real handler and logs the user in
- [ ] `go test ./...` all green (including DotStuffing)
- [ ] at least one telemetry event visible in local dev
- [ ] proxy auth tests added and passing
- [ ] share link redemption path tested

---

## Phase 1 — exe.dev Core UX Loop (after Phase 0)

_Minimum lovable parity: SSH in → create VM → run app → get HTTPS URL → control access → script it_

- [ ] **1.1: Normalize command surface** — audit names/args/help against exe.dev; add `new --env`, `new --command`, `new --prompt`, `ssh-key rename`, `share show`, `share add-link`, `share remove-link`, `share port`, `share set-public`, `share set-private`, `share receive-email`, `share access`; add `--json` flag consistently
- [ ] **1.2: VM provisioning polish** — thread Env through cmdNew → Manager.CreateAndStart() → VMStartOptions (plumbing exists but is disconnected); add container-command override; add prompt-on-create bootstrapper; instrument provisioning latency
- [ ] **1.3: HTTPS app-hosting loop** — canonical per-VM URL scheme; primary proxied port semantics; public/private flip; forwarded identity headers; request correlation logs in auth proxy

---

## Phases 2–7 (planned, not started)

| Phase | Focus |
|-------|-------|
| 2 | Sharing + browser-auth parity (share model, login-with-ussycode, VM-domain auth routes) |
| 3 | API + signed-token parity (permission enforcement, VM-scoped tokens, bearer/basic auth) |
| 4 | Email parity (inbound receive-email toggle, outbound audit logs, SMTP test hardening) |
| 5 | Agent-native experience (ussyuntu default image, Shelley-equivalent strategy, LLM gateway docs) |
| 6 | Teams + quota semantics (team model, per-user overrides, whoami --quotas) |
| 7 | Multi-node reality (real agent join/run, scheduler integration, WireGuard mesh completion) |

Full phase details: `PLAN-exe-dev-parity-roadmap.md`

---

## Key Files

| File | Purpose |
|------|---------|
| `PLAN-exe-dev-parity-roadmap.md` | Full parity roadmap with all phases and tasks |
| `spec.md` | Product specification |
| `docs/architecture.md` | System architecture with ASCII diagrams |
| `cmd/ussycode/main.go` | Entry point — P0-1 wiring fix is here (line 142) |
| `internal/ssh/browser.go` | Browser command — P0-2 URL mismatch is here (line 38) |
| `internal/gateway/email_test.go` | SMTP tests — P0-3 DotStuffing failure is here (line 395) |
| `internal/proxy/auth.go` | Auth proxy — P0-5 zero tests, P0-7 no share-link redemption |
| `PROGRESS-{A,B,C,E,F,G}.md` | Per-track completion details |
