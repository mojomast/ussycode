# PLAN-exe-dev-parity-roadmap.md

## Goal

Bring `ussycode` substantially closer to the practical end-state of **replicating exe.dev's product experience** while preserving its self-hosted / Ussyverse identity.

This plan is based on:
- live CLI reconnaissance against `exe.dev` via `ssh exe.dev help` and `ssh exe.dev help <command>`
- a site crawl of `https://exe.dev` documentation
- direct comparison against the current `ussycode` repository state
- current verified issues in `ussycode` (API wiring, browser auth mismatch, failing gateway test, incomplete agent join path, partial cluster wiring)
- **independent codebase verification** (2026-03-20): all claims verified against 62 Go files, 80+ tests, 7 PROGRESS docs. See corrections log below.

This document is a **plan only**. It does **not** implement changes.

### Corrections applied (2026-03-20 review)

The following corrections were made after independent verification against the actual codebase:

1. **Blocker 6 revised**: README is current (not stale) — uses correct `ussycode` naming. Changed to "needs parity roadmap restructuring."
2. **Blocker 1 expanded**: API handler passes nil for executor AND KeyResolver AND Config (all three, not just executor).
3. **Section 3.1B upgraded**: LLM gateway described as "substantially complete" (5 providers, BYOK, rate limiting, usage tracking, SSE) instead of "roughly correct."
4. **Section 3.1D upgraded**: API token system (usy0/usy1) described as fully implemented with current limitations noted.
5. **Section 2.3 added**: Development track completion status showing Tracks A, B, E, F, G all complete; Track C 1/5 done.
6. **Phase 0 expanded**: Added 4 new tasks (wire KeyResolver/Config, audit_logs, proxy tests, NetworkManager interface).
7. **Phase 1.2 updated**: Documented that `new` only supports `--name`/`--image`; VMStartOptions.Env exists but is disconnected.
8. **Phase 2.1 updated**: Added current share model state including missing RemoveShareLink and proxy link-token redemption gap.
9. **Phase 3.2 updated**: Token system described as implemented; focus shifted to permission enforcement and context injection.
10. **Phase 5.3 updated**: LLM gateway described as substantially complete; scope narrowed to hardening/docs.
11. **Phase 6.1 updated**: Quota system described as implemented; scope narrowed to team extension.
12. **Phase 6.2 updated**: Noted that zero team infrastructure exists (greenfield).
13. **Milestone A expanded**: Added 3 new prerequisite tasks.

---

## 1. Executive Summary

### 1.1 What exe.dev actually is

exe.dev is not "just VMs over SSH." It is a tightly integrated product made of:

- **SSH-first VM lifecycle management**
- **persistent, normal Linux machines**
- **automatic HTTPS proxying with auth-aware sharing**
- **human-usable and agent-usable APIs**
- **a default opinionated image** (`exeuntu`) that includes agent tooling
- **browser login / magic-link based web UX**
- **per-VM app sharing + public/private control + share links + email invites**
- **built-in metadata-side LLM and email gateways**
- **agent-centric workflows** via Shelley, AGENTS.md, prompt-on-create, and direct agent hosting
- **clear hosted-product operational constraints** (resource pools, pricing, teams, SSO, burst semantics)

### 1.2 What ussycode currently is

`ussycode` already has meaningful overlap with exe.dev:

- SSH gateway and REPL command surface
- SQLite-backed state model
- VM manager, image handling, networking, proxy/auth concepts
- metadata service
- admin panel package
- API package
- magic-token DB support
- LLM/email gateway packages
- templates, tutorial, arena, deployment assets, and docs

But it does **not** yet deliver exe.dev parity because several critical product flows are still incomplete, mismatched, or only partially wired.

### 1.3 Key product gap in one sentence

`ussycode` has many of the **components** of exe.dev, but not yet the **cohesive, verified product loop** that makes exe.dev feel like one seamless system.

---

## 2. Research Findings: Desired End State

## 2.1 Live exe.dev CLI command tree

### Verified top-level commands from `ssh exe.dev help`

```text
help
 doc
 ls
 new
 rm
 restart
 rename
 tag
 cp
 share
 whoami
 ssh-key
 shelley
 browser
 ssh
 exit
```

### Verified `share` subcommands

```text
share show
share port
share set-public
share set-private
share add
share remove
share add-link
share remove-link
share receive-email
share access
```

### Verified `ssh-key` subcommands

```text
ssh-key list
ssh-key add
ssh-key remove
ssh-key rename
```

### Verified `shelley` subcommands

```text
shelley install
shelley prompt
```

### Notable behavior / UX patterns inferred from CLI

- command names are short, memorable, and shell-native
- JSON mode is pervasive (`--json` on many commands)
- product UX is designed for both humans and automation
- shell help is documentation-quality, not just parser output
- `new` supports:
  - custom image
  - env injection
  - name override
  - container command override
  - prompt-on-create for agent bootstrapping
- `share` includes **public/private**, **invite by email**, **link sharing**, **port selection**, **inbound email**, and **team access control**
- browser login is treated as a first-class capability
- agent lifecycle is a core user story, not an afterthought

### Source
- live output from `ssh exe.dev help`
- live output from `ssh exe.dev help <command>`

---

## 2.2 Product semantics from exe.dev docs

### Core product promises

From `https://exe.dev/` and `what-is-exe`:

- persistent disks
- normal Linux machines
- fast provisioning
- HTTPS by default
- auth handled by the platform
- agent-friendly environments
- ability to clone environments quickly
- shared underlying resources across multiple VMs

### Networking and web exposure model

From `proxy`, `sharing`, `cnames`, `login-with-exe`:

- platform terminates TLS and proxies to per-VM app ports
- private by default, public if explicitly changed
- alternate internal ports remain access-controlled unless the primary public port is selected
- users can share access via:
  - public mode
  - direct email invite
  - share links
- custom domains support subdomain and apex-domain mapping
- identity can be forwarded to apps using HTTP headers like:
  - `X-ExeDev-UserID`
  - `X-ExeDev-Email`
- special auth routes exist at the VM domain level:
  - `__exe.dev/login`
  - `__exe.dev/logout`

### API model

From `api` and `https-api`:

- **primary API is SSH itself**
- HTTPS API is simply `POST /exec` with SSH-style command bodies
- JSON output is automatic in the HTTPS API path
- auth tokens are signed locally with SSH private keys
- auth model supports:
  - bearer token to platform API
  - bearer/basic auth to VM proxy
  - context-carrying signed VM tokens
  - short opaque tokens derived from long signed tokens
- permissions can be scoped by commands and expiry

### Email model

From `receive-email` and `send-email`:

- inbound mail can be toggled per VM
- inbound mail goes to `~/Maildir/new/`
- mail is delivered to VM-name-based addresses
- backlog safety limits exist
- outbound mail is allowed to the VM owner via metadata-side gateway
- outbound is explicitly rate-limited

### Agent model

From `shelley/*`, `use-case-agent`, `use-case-openclaw`, `guts`, `agents-md`:

- default image includes agent-oriented tooling
- agent is web-facing and mobile-usable
- LLM gateway is built into the platform at metadata service addresses
- BYOK exists for agent/provider override
- AGENTS.md is part of product behavior
- users are expected to run OpenClaw / Codex / Claude-style agents on these VMs
- default image is opinionated, productive, and application-oriented

### Infrastructure model

From `faq/how-exedev-works`:

- hosted product currently uses **Cloud Hypervisor**
- VM boot is container-image based rather than traditional long-lived VM image building
- HTTPS reverse proxy and SSH routing hide lack of public per-VM IPs
- there is **not** a built-in east-west private network between VMs by default

### Important product note for parity planning

`ussycode` does **not** need to copy exe.dev's exact infrastructure choices (e.g. Cloud Hypervisor) to replicate its **product semantics**. It does need to replicate the user-facing capabilities and reliability guarantees.

---

## 2.3 Development track completion status

Per PROGRESS-A.md through PROGRESS-G.md, the following tracks are complete:

| Track | Description | Status | Tests |
|-------|-------------|--------|-------|
| A | Core Hardening (rename, config, ZFS, nftables) | ✅ COMPLETE | 14 ZFS + 10 nftables |
| B | Ussyverse Server Pool (gRPC proto, agent, PKI, heartbeat, WireGuard stub, scheduler, installer) | ✅ COMPLETE | 7 PKI + 10 scheduler |
| C | UX & Onboarding | ⚠️ 1/5 DONE | Tutorial done; browser/doc/templates/MOTD pending |
| D | Gateway Services | (merged into Track E) | - |
| E | API & Admin (HTTPS API, admin panel, trust/quotas, custom domains) | ✅ COMPLETE | 18 API + 27 admin + 7 quota + 8 domain |
| F | Ussyverse Integration (arena, templates, branding) | ✅ COMPLETE | Arena tests pass |
| G | Deployment & Ops (Ansible, installers, docs) | ✅ COMPLETE | No Go files modified |

**Important**: 5 of 6 tracks are complete. 80+ tests across 12 suites. The parity roadmap builds on top of this foundation - it does not need to re-implement these subsystems.

---

## 3. Current-State Gap Analysis

## 3.1 Areas where ussycode is already directionally aligned

### A. SSH-first command product
Relevant files:
- `internal/ssh/gateway.go`
- `internal/ssh/shell.go`
- `internal/ssh/commands.go`
- `internal/ssh/browser.go`
- `internal/ssh/tutorial.go`
- `internal/ssh/community.go`
- `internal/ssh/arena.go`

Status:
- command-oriented shell exists
- many exe.dev-like verbs exist already (`new`, `ls`, `rm`, `restart`, `rename`, `tag`, `cp`, `share`, `ssh-key`, `browser`, `ssh`)

### B. Metadata service and built-in platform gateways
Relevant files:
- `internal/gateway/metadata.go`
- `internal/gateway/llm.go`
- `internal/gateway/email.go`
- `internal/gateway/email_send.go`

Status:
- fully aligned with exe.dev's metadata-side gateway pattern
- **LLM gateway is substantially complete**: 5 providers (Anthropic, OpenAI, Fireworks, Ollama, VLLM), BYOK with AES encryption, per-user token bucket rate limiting, usage tracking in DB, SSE streaming passthrough - all tests pass
- **Inbound email is substantially complete**: SMTP server with Maildir delivery, per-VM rate limiting (100/hr), unread quota enforcement (1000) - 8/9 tests pass (only DotStuffing test fails)
- **Outbound email is substantially complete**: owner-only restriction, SMTP relay, per-VM rate limiting (10/hr), RFC 2822 formatting - all tests pass
- metadata service serves AWS-style metadata, VM/user metadata, SSH keys, env, and proxies to LLM/email gateways

### C. Web access + auth proxy concepts
Relevant files:
- `internal/proxy/caddy.go`
- `internal/proxy/auth.go`
- `internal/admin/admin.go`
- `internal/ssh/browser.go`

Status:
- reverse proxy + auth proxy + browser access concepts exist
- custom-domain support exists in current progress docs / command layer

### D. API philosophy
Relevant files:
- `internal/api/handler.go`
- `docs/api.md`

Status:
- ussycode mirrors exe.dev's "SSH commands over HTTP" model
- both token formats are implemented: `usy0.*` (stateless, SSH-signed with exp/nbf/perms/nonce/ctx) and `usy1.*` (DB-backed opaque)
- POST /exec, GET /health, GET /version endpoints exist
- per-fingerprint rate limiting (60 req/min) implemented
- 14+ API tests exist with mock executor
- **runtime wiring is broken**: executor, KeyResolver, and Config are all passed as nil in main.go

### E. Agent-centric direction
Relevant files:
- `internal/gateway/llm.go`
- `templates/*`
- `internal/ssh/commands.go`
- `internal/ssh/browser.go`
- `internal/ssh/community.go`
- `internal/ssh/arena.go`

Status:
- repo is pointed toward agent hosting and Ussyverse workflows
- Shelley-equivalent semantics are not yet cohesive

---

## 3.2 Critical parity blockers

### Blocker 1 - API path is present but not truly operational
Relevant files:
- `cmd/ussycode/main.go` (line 162)
- `internal/api/handler.go`

Problem:
- `api.NewHandler(database, nil, nil, logger, nil)` is wired with nil executor, nil KeyResolver, AND nil Config
- `handleExec` directly calls `h.exec.Execute(...)` - nil pointer panic at runtime
- rate limiting config is not passed (nil Config)
- token verification for `usy0.*` tokens cannot resolve SSH public keys (nil KeyResolver)
- parity with exe.dev's HTTPS API is therefore blocked at runtime

Impact:
- ussycode cannot yet honestly claim exe.dev-like programmatic API behavior
- ALL three nil arguments must be wired, not just the executor

### Blocker 2 - Browser login / magic-link flow is inconsistent
Relevant files:
- `internal/ssh/browser.go`
- `internal/admin/admin.go`
- `internal/db/queries.go`

Problem:
- browser command creates a token and emits `https://<domain>/__auth/magic/<token>`
- admin panel expects `/admin/login/callback?token=...`
- no verified bridge handler exists for the generated path

Impact:
- browser-based product parity is broken
- first-class web UX cannot be trusted end-to-end

### Blocker 3 - VM sharing semantics do not yet match exe.dev's full model
Relevant files:
- `internal/ssh/commands.go`
- `internal/proxy/auth.go`
- `internal/proxy/caddy.go`
- `internal/db/models.go`
- `internal/db/queries.go`

Problem:
- ussycode supports several share concepts, but current naming and semantics differ from exe.dev
- email invites, link shares, public/private, team access, port selection, and inbound email need to be normalized into one coherent model

Impact:
- web sharing parity is incomplete
- auth-aware app-hosting UX remains fragmented

### Blocker 4 - Storage/runtime story is internally inconsistent
Relevant files:
- `internal/storage/zfs.go`
- `internal/vm/manager.go`
- `internal/vm/image.go`

Problem:
- the code contains a StorageBackend abstraction, but VM manager still uses direct ext4 file copying / disk creation flows
- current runtime behavior does not cleanly reflect the "persistent, cloneable disk" product story

Impact:
- parity with exe.dev's fast clone / persistent disk / predictable storage semantics remains uncertain

### Blocker 5 - Agent experience is missing a default-image + control-surface unification layer
Relevant files:
- `images/ussyuntu/*`
- `templates/*`
- `internal/gateway/llm.go`
- `internal/ssh/commands.go`

Problem:
- ussycode has pieces of an agent platform, but not a single coherent default "this VM is ready for agents immediately" story comparable to exe.dev + Shelley

Impact:
- agent-hosting parity remains conceptual, not productized

### Blocker 6 - Current docs do not describe the parity roadmap
Relevant files:
- `README.md`
- `docs/*`

Problem:
- README is current and uses `ussycode` naming (the rename from `exedevussy` was completed in Track A.1), but it does not surface the exe.dev-parity roadmap or product positioning
- 5 comprehensive docs exist (api, architecture, getting-started, self-hosting, contributing-compute) but none describe the parity target
- users and contributors cannot infer the parity roadmap from current docs

Impact:
- moderate onboarding friction for parity-aware contributors (docs themselves are fine for general use)

---

## 4. Product Principles for exe.dev Parity

The implementation roadmap should be guided by these principles.

### 4.1 Match product semantics first, infrastructure second

Prioritize parity in:
- provisioning UX
- persistent disk semantics
- clone/copy behavior
- share/auth flows
- HTTP proxy behavior
- browser login behavior
- API shape
- agent readiness

Do **not** block parity work on switching hypervisors unless Firecracker specifically prevents the product behavior.

### 4.2 Keep SSH as the canonical interface

exe.dev's strongest idea is that the product's human API and automation API are the same command model.

Implication for ussycode:
- every important operation should be available in the SSH CLI first
- HTTP API should remain a thin transport over that command model
- admin UI should use the same underlying policies and capabilities, not a parallel hidden control plane

### 4.3 Treat HTTPS exposure as a first-class feature, not an add-on

exe.dev parity requires that "run app → get URL → share URL → control access" feel native.

Implication:
- proxy selection, auth, public/private state, port behavior, share links, and custom domains must become a single cohesive subsystem

### 4.4 Make the default image opinionated and valuable

exe.dev's default image is not empty infrastructure - it is a productive agent/developer environment.

Implication:
- `ussyuntu` needs to become clearly competitive with `exeuntu`
- initial prompt / agent support / app-server defaults should feel intentional

### 4.5 Telemetry must be part of parity work

Per project instructions, all new work must include telemetry.

Implication:
- every new user-facing operation in this roadmap needs structured logs, latency metrics, and identifiable request/user/VM context
- `/exec`, proxy auth, share changes, token issuance, VM lifecycle, email delivery, and browser-login flows must all be instrumented

---

## 5. Roadmap Overview

## Phase 0 - Close present contradictions before parity work

This phase is mandatory. Do not build more parity features on top of broken core flows.

### Objectives
- make current product claims true
- remove obvious runtime mismatches
- establish a reliable baseline for parity work

### Tasks
- [x] Fix `POST /exec` runtime wiring by providing a real command executor to `internal/api/handler.go`
- [x] Also wire KeyResolver and Config (both nil) into the API handler - rate limiting and `usy0.*` token verification depend on these
- [x] Introduce a shared command execution layer used by SSH shell and HTTP API
- [x] Fix browser magic-link flow so generated URLs land in a real, verified auth path (browser.go emits `/__auth/magic/<token>` but no handler exists; admin expects `/admin/login/callback?token=...`)
- [ ] Decide whether admin login and general browser login share the same token type or use separate paths
- [x] Fix `internal/gateway` failing `TestSMTPServer_DotStuffing` (Maildir path issue in test setup)
- [x] Restructure `README.md` to surface parity roadmap (current README uses correct naming but doesn't describe parity goals)
- [x] Re-run `go build ./...` and `go test ./...` until green baseline is restored (currently only 1 test fails)
- [x] Add telemetry foundation - OTEL/Maple ingest wiring, instrument `/exec`, browser token creation/consumption, and SMTP delivery failures (zero observability exists today)
- [x] Wire audit_logs table into admin operations (migration 007 created the table but no code writes to it)
- [x] Add tests for `internal/proxy` package (zero test coverage on critical auth path)
- [ ] Extract NetworkManager interface to match StorageBackend/FirewallManager pattern for testability

### Validation
- [ ] `curl -X POST /exec` works with a real executor, KeyResolver, and Config
- [ ] browser-generated URL logs user in successfully and predictably
- [ ] `go test ./...` passes (all packages)
- [ ] smoke test documented for API + browser + SMTP paths
- [ ] telemetry events visible in local dev for at least one instrumented path

---

## Phase 1 - Match exe.dev's core day-1 user experience

This phase is about the **minimum lovable parity loop**:

> SSH in → create VM → SSH into it → run app → get HTTPS URL → control access → script it

### 1.1 Normalize the command surface to exe.dev parity where appropriate

Relevant files:
- `internal/ssh/commands.go`
- `internal/ssh/browser.go`
- `internal/ssh/tutorial.go`
- `docs/getting-started.md`

Tasks:
- [ ] Audit command names, arguments, and help output against exe.dev
- [ ] Add missing parity behaviors for:
  - [ ] `new --env`
  - [ ] `new --command`
  - [ ] `new --prompt`
  - [ ] `ssh-key rename`
  - [ ] `share show`
  - [ ] `share add-link`
  - [ ] `share remove-link`
  - [ ] `share port`
  - [ ] `share set-public`
  - [ ] `share set-private`
  - [ ] `share receive-email`
  - [ ] `share access allow|disallow`
- [ ] Decide which exe.dev commands should map directly and which should intentionally remain Ussyverse-specific
- [ ] Ensure help text is polished and script-friendly
- [ ] Ensure `--json` behavior is available and consistent across all major commands

Validation:
- [ ] `help` output documents parity-grade commands and options
- [ ] command help pages mirror actual runtime behavior
- [ ] JSON mode contract is documented and stable

### 1.2 Make VM provisioning feel like "just give me a computer"

Relevant files:
- `internal/ssh/commands.go` (cmdNew at line 172)
- `internal/vm/manager.go`
- `internal/vm/firecracker.go` (VMStartOptions.Env field exists at line 261 but is unused)
- `internal/vm/image.go`
- `images/ussyuntu/*`

Current state (verified):
- `new` supports only `--name=` and `--image=` flags
- VM defaults: 1 vCPU, 512MB RAM, 5GB disk, image "ussyuntu"
- VMStartOptions struct has an `Env` field but Manager.CreateAndStart() doesn't accept or pass environment variables
- No `--command` or `--prompt` support exists at any layer
- Quota enforcement works (trust-level-based VM limits)
- Proxy route auto-registration on creation works

Tasks:
- [ ] Verify `new` defaults are fast and predictable
- [ ] Add env injection support - thread Env from cmdNew through Manager.CreateAndStart() to VMStartOptions (plumbing exists in Firecracker backend but is disconnected)
- [ ] Add container-command override behavior if image-driven boot supports it
- [ ] Add prompt-on-create flow that bootstraps default agent environment or first-run automation
- [ ] Ensure created VM has a stable SSH destination and predictable public URL
- [ ] Expose provisioning failures with clear, user-readable messages
- [ ] Instrument create/start timing from command issue → usable VM

Validation:
- [ ] `new` creates a ready-to-use VM with minimal arguments
- [ ] prompt-on-create can bootstrap a sample app or agent task
- [ ] provisioning telemetry shows success/failure and latency

### 1.3 Bring the HTTPS app-hosting loop to parity

Relevant files:
- `internal/proxy/caddy.go`
- `internal/proxy/auth.go`
- `internal/ssh/commands.go`
- `internal/db/models.go`
- `internal/db/queries.go`

Tasks:
- [ ] Define canonical per-VM URL scheme and document it
- [ ] Implement single primary proxied port semantics
- [ ] Implement alternate port forwarding behavior for allowed ranges if desired
- [ ] Add/verify automatic port discovery strategy
- [ ] Ensure auth proxy supports private-by-default web access
- [ ] Ensure public mode is explicit and reversible
- [ ] Ensure proxy passes correct forwarded headers
- [ ] Add request correlation logs for auth proxy decisions

Validation:
- [ ] app on VM becomes reachable over HTTPS without manual Caddy config
- [ ] public/private flips work correctly
- [ ] proxy headers are documented and tested

---

## Phase 2 - Match exe.dev's sharing and browser-auth model

This is the highest product-value parity layer after basic VM creation.

### 2.1 Unify sharing into one coherent subsystem

Relevant files:
- `internal/ssh/commands.go`
- `internal/proxy/auth.go`
- `internal/db/models.go`
- `internal/db/queries.go`
- `internal/admin/admin.go`

Current state (verified):
- Share model exists with VMID, SharedWith (user), LinkToken, IsPublic fields
- DB has: ShareVMWithUser, ShareVMWithLink, RemoveShare, SharesByVM, ShareByLinkToken, SetVMPublic, IsVMPublic, HasShareAccess
- **Missing from DB**: no RemoveShareLink function (links can be created but never revoked)
- **Missing from proxy**: auth proxy checks ownership and share-by-user but has **no flow for redeeming a share link token and granting a session** - ShareByLinkToken exists in DB but proxy.auth.go never calls it
- Custom domain support already exists (share cname, cname-verify, cname-rm) - this is a parity advantage

Tasks:
- [ ] Define a single share model covering:
  - [ ] owner-only access
  - [ ] public access
  - [ ] invite-by-email access
  - [ ] share-link access
  - [ ] team access
  - [ ] SSH/Shelley/team-access distinctions
- [ ] Ensure command names and DB schema align with this model
- [ ] Add link lifecycle operations and proper revocation semantics
- [ ] Define what access is granted by link redemption versus direct invite
- [ ] Decide whether accepted link access is durable or temporary
- [ ] Add audit logs for all share mutations

Validation:
- [ ] full share matrix is documented and testable
- [ ] invites, links, and public mode behave distinctly and correctly
- [ ] access rules are enforced consistently by proxy and SSH layer

### 2.2 Implement "Login with ussycode" parity at the VM domain level

Relevant files:
- `internal/proxy/auth.go`
- `internal/ssh/browser.go`
- `internal/admin/admin.go`
- `internal/db/queries.go`

Tasks:
- [ ] Add VM-domain auth endpoints analogous to exe.dev's `__exe.dev/login` and logout flows
- [ ] Decide namespacing for special auth routes (e.g. `__ussycode/login`)
- [ ] Inject stable user identity headers to proxied apps
- [ ] Document the header contract for applications
- [ ] Support both public proxies with optional login and private proxies with mandatory login
- [ ] Add local-dev testing helpers or sample middleware docs for app developers

Validation:
- [ ] a proxied app can reliably identify logged-in users from headers
- [ ] login/logout round-trip works on VM domains
- [ ] headers are absent when expected and present when expected

### 2.3 Make browser access truly first-class

Relevant files:
- `internal/ssh/browser.go`
- `internal/admin/admin.go`
- `internal/proxy/auth.go`

Tasks:
- [ ] Build one canonical browser-access flow from SSH command to session establishment
- [ ] Add QR output only if it becomes genuinely useful and reliable
- [ ] Ensure browser sessions are secure, short-lived where appropriate, and revocable
- [ ] Decide whether browser login lands on admin panel, user dashboard, or VM web surface first
- [ ] Add telemetry around token issuance, redemption, expiry, and invalid use

Validation:
- [ ] `browser` command works predictably for primary user journeys
- [ ] no dead-end or mismatched routes remain

---

## Phase 3 - Match exe.dev's API and auth-token sophistication

## 3.1 Fully realize the SSH-as-API model

Relevant files:
- `internal/api/handler.go`
- `internal/auth/token.go`
- `docs/api.md`
- `docs/self-hosting.md`

Tasks:
- [ ] Preserve the SSH command model as the canonical API contract
- [ ] Ensure HTTPS API request body semantics match actual SSH command parsing
- [ ] Make JSON output behavior consistent and default where appropriate for HTTP
- [ ] Provide documented response codes and stable error payloads
- [ ] Add exhaustive integration tests for `/exec`

Validation:
- [ ] shell commands and HTTP API produce meaningfully equivalent results
- [ ] docs match runtime behavior exactly

## 3.2 Reach exe.dev-style signed-token parity

Relevant files:
- `internal/auth/token.go`
- `internal/api/handler.go` (lines 237-325: both usy0/usy1 fully implemented)
- `internal/proxy/auth.go`
- `docs/api.md`

Current state (verified):
- `usy0.*` stateless tokens: SSH-signed, supports exp, nbf, perms (string array), nonce, ctx (user handle) - implemented in handler.go:246-291
- `usy1.*` DB-backed tokens: opaque ID lookup, last-used tracking, revocation - implemented in handler.go:322-340
- Token creation, verification, and handle generation all have tests in auth/token_test.go
- Permission field (perms) exists in TokenPayload but **enforcement of command allowlists is not implemented** - perms are stored but not checked against the requested command

Tasks:
- [ ] Audit and compare `usy0`/`usy1` semantics against exe.dev's `exe0`/`exe1` for any missing capabilities
- [ ] Implement permission enforcement - check token's `perms` against requested command in handleExec
- [ ] Add context payload injection for downstream apps (the `ctx` field exists for user handle but not for arbitrary app context)
- [ ] Add VM-scoped token namespace for proxied HTTPS services
- [ ] Support bearer auth for VM proxy APIs
- [ ] Consider basic-auth token support for Git-over-HTTPS parity
- [ ] Decide whether short opaque handles are necessary now or later
- [ ] Add docs and helper scripts for token creation

Validation:
- [ ] `/exec` token flow works end-to-end
- [ ] VM-proxy bearer auth works end-to-end
- [ ] downstream app receives signed context header if configured

---

## Phase 4 - Match exe.dev's email and app-integration story

## 4.1 Inbound email parity

Relevant files:
- `internal/gateway/email.go`
- `internal/ssh/commands.go`
- `docs/*`

Tasks:
- [ ] Make `share receive-email <vm> on|off` a canonical command path
- [ ] Ensure delivery address format is explicit and documented
- [ ] Deliver to `~/Maildir/new/` exactly and reliably
- [ ] Add backlog safety disablement logic if not already equivalent
- [ ] Fix and expand SMTP tests
- [ ] Document limitations clearly

Validation:
- [ ] toggling receive-email changes actual delivery behavior
- [ ] mail lands in correct Maildir path
- [ ] overload behavior is safe and recoverable

## 4.2 Outbound owner-only email parity

Relevant files:
- `internal/gateway/email_send.go`
- `internal/gateway/metadata.go`

Tasks:
- [ ] Ensure metadata-side send-email endpoint is stable
- [ ] Restrict `to` to VM owner email (or explicit safe policy)
- [ ] Add rate-limiting and owner enforcement tests
- [ ] Add structured audit logs for sends, denials, and limits

Validation:
- [ ] VM can send owner email through metadata endpoint
- [ ] abuse controls are observable and effective

---

## Phase 5 - Match exe.dev's agent-native experience

This is where ussycode should stop merely resembling exe.dev infrastructure and start resembling exe.dev's practical agent cloud.

## 5.1 Upgrade `ussyuntu` into a true `exeuntu` competitor

Relevant files:
- `images/ussyuntu/Dockerfile`
- `images/ussyuntu/*`
- `templates/*`
- `docs/getting-started.md`

Tasks:
- [ ] Define the default image contract explicitly
- [ ] Preinstall and validate core agent tooling
- [ ] Make default services predictable and documented
- [ ] Add first-run UX for agent workflows
- [ ] Ensure app servers on common ports are easy to expose
- [ ] Add example templates for agent-centric workloads

Validation:
- [ ] fresh VM feels immediately useful for AI-assisted development
- [ ] agent examples run without manual platform spelunking

## 5.2 Decide on the Shelley-equivalent strategy

Options:
- **Option A:** build a first-party browser agent integrated into ussycode
- **Option B:** deeply support third-party agents (Pi/OpenClaw/Codex/Claude/etc.) and keep browser UI minimal
- **Option C:** do both, but in phases

Recommended near-term choice:
- **Option B first**, because the current repo already has agent-friendly substrate pieces and can reach useful parity faster by making external agents first-class

Tasks:
- [ ] Define a standard "agent-ready VM" capability checklist
- [ ] Add guidance-file support parity (`AGENTS.md` / equivalents) in docs and templates
- [ ] Add example workflows for running coding agents on ussycode VMs
- [ ] Consider a `prompt-on-create` bootstrapper for agents
- [ ] Ensure LLM gateway works smoothly from the default image

Validation:
- [ ] sample "new + prompt + build app" flow works
- [ ] sample "run external agent on VM" guide works end-to-end

## 5.3 LLM gateway parity and quality

Relevant files:
- `internal/gateway/llm.go`
- `internal/ssh/commands.go` (llm-key command at line 1562)
- `docs/*`

Current state (verified):
- 5 providers supported: Anthropic, OpenAI, Fireworks, Ollama, VLLM
- BYOK with AES-GCM encryption - keys stored encrypted in DB per user/provider
- Per-user token bucket rate limiting (configurable)
- Usage tracking (requests + token estimates) stored in DB
- SSE streaming passthrough with -1 FlushInterval
- `llm-key` command registered (set/list/rm subcommands) - all functional
- All LLM gateway tests pass
- metadata service routes: `/gateway/llm/{provider}` proxies to LLM gateway

Tasks:
- [ ] Decide whether the default gateway should be subscription-backed, BYOK-only, or hybrid
- [ ] Document endpoint usage from inside VMs clearly (curl examples for each provider)
- [ ] Add usage tracking telemetry (usage already tracked in DB - needs OTEL export)
- [ ] Harden key-management UX (key rotation, provider validation)
- [ ] Confirm gateway behavior from default image and from agent tooling

Validation:
- [ ] curl examples work from inside VMs
- [ ] BYOK and platform-key modes are both clearly defined

---

## Phase 6 - Product-operational parity: teams, quotas, plans, hosted assumptions

`ussycode` is self-hosted and Ussyverse-driven, but to replicate exe.dev's practical end-state it still needs a clearer product model for hosted usage.

## 6.1 Clarify resource and quota semantics

Relevant files:
- `internal/db/models.go`
- `internal/db/queries.go`
- `internal/ssh/commands.go`
- `internal/admin/admin.go`

Current state (verified):
- 4-tier trust system already implemented: newbie/citizen/operator/admin
- Per-tier quotas: VM limit, CPU limit, RAM limit, disk limit - enforced in cmdNew
- SetUserTrustLevel correctly updates both trust_level AND all 4 quota columns
- Admin CLI command for trust level changes exists
- Over-quota error messages exist ("VM limit reached (X/Y). Upgrade trust level or remove a VM.")
- 7 quota tests pass

Tasks:
- [ ] Decide whether quotas should extend to per-team in addition to per-user
- [ ] Decide whether VMs share aggregate CPU/RAM/disk pools like exe.dev
- [ ] Add visible quota introspection command (e.g. `whoami --quotas` or `quota`)
- [ ] Add admin controls for per-user quota overrides beyond trust-level defaults

Validation:
- [ ] users can understand what resources they have and why creation fails

## 6.2 Introduce a coherent team model

Current state: **No team infrastructure exists.** No team model, no migration, no queries, no DB table. This is greenfield work.

Tasks:
- [ ] define team entity and membership model in DB (new migration required)
- [ ] define share semantics for `team`
- [ ] define admin/operator abilities inside teams
- [ ] wire team access into proxy/auth decisions
- [ ] decide whether team burst capacity exists and how it's enforced

Validation:
- [ ] team sharing works consistently across SSH, web proxy, and admin views

## 6.3 Decide where hosted-product features stop

Potential parity items to defer or adapt:
- full hosted billing
- SSO
- enterprise VPC integration
- usage-based enterprise plans

Plan guidance:
- self-hosted parity does not require billing parity immediately
- team/admin model should come before billing
- billing can be abstracted as future hosted mode if desired

---

## Phase 7 - Multi-node and infra alignment

This phase matters if the real end-goal is not only UX parity with exe.dev, but also scalable hosted or community-contributed compute.

## 7.1 Decide whether Firecracker remains the runtime or whether Cloud Hypervisor parity matters

Context:
- exe.dev uses Cloud Hypervisor today
- ussycode uses Firecracker abstractions already

Recommendation:
- keep Firecracker unless it materially blocks parity in:
  - startup speed
  - disk semantics
  - container-image-backed rootfs behavior
  - SSH/proxy model
  - operator ergonomics

Tasks:
- [ ] benchmark create/start/clone flows against parity goals
- [ ] identify any Firecracker-specific blockers to exe.dev-like UX
- [ ] only consider hypervisor swap if UX parity demands it

## 7.2 Finish cluster truthfully, not cosmetically

Relevant files:
- `internal/controlplane/nodemanager.go`
- `internal/scheduler/scheduler.go`
- `internal/mesh/*`
- `internal/agent/*`
- `cmd/ussyverse-agent/main.go`

Tasks:
- [ ] implement real control-plane client transport for agent join/run
- [ ] connect heartbeat command handling to VM management
- [ ] replace or finish stub WireGuard path
- [ ] define scheduler integration into actual placement flow
- [ ] define whether VMs can migrate, or only be rescheduled on recreate
- [ ] add end-to-end multi-node test harness

Validation:
- [ ] agent can join, heartbeat, receive commands, and host a VM
- [ ] scheduler chooses nodes for real VM placements
- [ ] node health transitions trigger usable operator behavior

## 7.3 Decide on east-west networking philosophy

exe.dev docs explicitly say no built-in private VM network by default.

Recommendation:
- do **not** make a private mesh the default user model just because cluster infrastructure exists
- keep VMs isolated by default
- provide optional overlays (e.g. Tailscale) for users who want private connectivity

Tasks:
- [ ] document default isolation model clearly
- [ ] decide whether current cluster networking is control-plane-only or user-facing
- [ ] avoid exposing unnecessary implicit connectivity between VMs

---

## 6. Documentation and product-narrative work

This is not polish. It is necessary to prevent ongoing cognitive debt.

### Tasks
- [ ] Rewrite `README.md` around the actual current product and the exe.dev-parity roadmap
- [ ] Add a dedicated `docs/parity-with-exe-dev.md` comparison doc
- [ ] Add a "hosted mode vs self-hosted mode" explanation if relevant
- [ ] Add docs for:
  - [ ] browser login
  - [ ] HTTP proxy and forwarded headers
  - [ ] Login with ussycode header contract
  - [ ] signed API token creation
  - [ ] VM auth tokens for proxied apps
  - [ ] inbound and outbound email
  - [ ] agent-ready workflows
- [ ] Make docs executable with copy-paste examples

Validation:
- [ ] a new contributor can explain the parity roadmap without reading old progress docs

---

## 7. Telemetry Plan (Mandatory)

Per repo instructions, parity work must be telemetry-first.

## Required instrumentation areas

### VM lifecycle
- [ ] create requested
- [ ] create started
- [ ] image resolved
- [ ] rootfs/disk ready
- [ ] network allocated
- [ ] boot success/failure
- [ ] proxy registration success/failure
- [ ] stop/restart/remove events

### API
- [ ] `/exec` request count, latency, success/failure
- [ ] command name distribution
- [ ] auth failures by reason
- [ ] token verification failures

### Proxy/auth
- [ ] auth proxy allow/deny decisions
- [ ] reason codes (public, owner, shared, token, denied)
- [ ] share-link redemption events
- [ ] browser-login token creation/redemption/expiry

### LLM gateway
- [ ] provider selection
- [ ] request latency
- [ ] error class
- [ ] user/VM correlation
- [ ] quota usage

### Email
- [ ] inbound delivery attempts + failures
- [ ] backlog auto-disable events
- [ ] outbound send attempts + rate-limit hits

### Cluster layer
- [ ] agent join attempts
- [ ] heartbeat health transitions
- [ ] scheduler decisions
- [ ] node-drain / node-dead events

### Expected pipeline
- [ ] instrument to Maple ingest / OTLP as required by project policy
- [ ] validate telemetry in local dev before marking each phase done

---

## 8. Recommended Implementation Order

### Milestone A — Baseline truthfulness
- [ ] Fix API executor + KeyResolver + Config wiring (all three nil)
- [ ] Fix browser login path (URL mismatch)
- [ ] Fix gateway test failure (DotStuffing)
- [ ] Restructure README around parity roadmap
- [ ] Wire audit_logs table into admin operations
- [ ] Add proxy auth tests (zero coverage)
- [ ] Add telemetry foundation (OTEL/Maple ingest)
- [ ] green build/test baseline

### Milestone B - Core exe.dev UX loop
- [ ] normalize SSH command parity
- [ ] finish `new` env/command/prompt semantics
- [ ] finish HTTPS proxy + public/private + port flow
- [ ] finish browser login and identity headers

### Milestone C - Sharing + token + API parity
- [ ] full share subsystem
- [ ] signed token parity for `/exec` and VM APIs
- [ ] docs + examples + integration tests

### Milestone D - Email + agent-native productization
- [ ] inbound/outbound email parity
- [ ] agent-ready default image
- [ ] LLM gateway hardening and docs

### Milestone E - Teams + hosted-product semantics
- [ ] team model
- [ ] admin controls
- [ ] quota/burst semantics

### Milestone F - Multi-node reality
- [ ] real agent join/run
- [ ] scheduler integration
- [ ] mesh transport completion
- [ ] end-to-end cluster tests

---

## 9. Validation Matrix

## 9.1 Parity smoke tests

### User journey smoke tests
- [ ] `ssh ussyco.de help` exposes polished command tree
- [ ] `ssh ussyco.de new --name demo` provisions usable VM
- [ ] `ssh ussyco.de ssh demo` connects successfully
- [ ] start app on VM, HTTPS URL works
- [ ] `share set-public demo` changes accessibility
- [ ] `share add demo user@example.com` grants private access
- [ ] `share add-link demo` creates login-gated share link
- [ ] `browser` command creates usable web login flow
- [ ] `curl POST /exec` works with signed token
- [ ] VM-level bearer token reaches app with identity/context headers
- [ ] `share receive-email demo on` delivers Maildir message
- [ ] metadata-side send-email can notify owner

### Agent journey smoke tests
- [ ] create default VM and verify agent tooling exists
- [ ] prompt-on-create bootstraps simple app
- [ ] AGENTS.md guidance works in chosen agent workflow
- [ ] metadata-side LLM gateway works via curl and via agent

### Operator journey smoke tests
- [ ] admin panel login works from browser path
- [ ] trust level change affects quotas
- [ ] custom domain mapping works end-to-end
- [ ] proxy auth logs show clear allow/deny reason

### Cluster journey smoke tests
- [ ] agent joins successfully
- [ ] node heartbeat visible
- [ ] scheduler places at least one VM to agent node
- [ ] node unhealthy path is observable and safe

---

## 10. Files Likely to Change by Phase

## Phase 0-3 core parity files
- `cmd/ussycode/main.go`
- `internal/api/handler.go`
- `internal/auth/token.go`
- `internal/ssh/commands.go`
- `internal/ssh/browser.go`
- `internal/proxy/auth.go`
- `internal/proxy/caddy.go`
- `internal/admin/admin.go`
- `internal/db/models.go`
- `internal/db/queries.go`
- `internal/gateway/email.go`
- `internal/gateway/email_send.go`
- `internal/gateway/metadata.go`
- `README.md`
- `docs/api.md`
- `docs/getting-started.md`
- `docs/self-hosting.md`

## Phase 4-5 agent/image files
- `images/ussyuntu/Dockerfile`
- `images/ussyuntu/init-ussycode.sh`
- `images/ussyuntu/*`
- `templates/*`
- additional docs under `docs/`

## Phase 6-7 team/cluster files
- `internal/controlplane/nodemanager.go`
- `internal/scheduler/scheduler.go`
- `internal/mesh/wireguard.go`
- `internal/mesh/allocator.go`
- `internal/agent/agent.go`
- `internal/agent/heartbeat.go`
- `cmd/ussyverse-agent/main.go`
- deployment assets under `deploy/`

---

## 11. Open Questions Requiring Product Decisions

These should be answered before or during Phase 1-3.

- [ ] Should `ussycode` keep its current command names where they differ from exe.dev, or prioritize command-level parity explicitly?
- [ ] Should browser auth primarily land in a general user web dashboard, the admin panel, or directly support VM domain auth first?
- [ ] Should ussycode implement a first-party Shelley-like browser coding agent, or focus on making third-party agents the canonical experience?
- [ ] Should the default image remain Ubuntu-oriented, or become more container/image-driven and opinionated like exe.dev's `exeuntu` story?
- [ ] Should VM proxy auth support both bearer and basic auth for git/tooling parity?
- [ ] Should team semantics be part of the near-term parity target, or follow after single-user parity is complete?
- [ ] Is the long-term product target a hosted service that competes directly with exe.dev, a self-hosted clone, or a Ussyverse-specific fork of the product idea?

---

## 12. Recommended Next Action

The highest-leverage next move is:

### Immediate next task
- [ ] Execute **Phase 0** as a single focused stabilization milestone

### Why this first
Because exe.dev parity work will be noisy and misleading until these current contradictions are fixed:
- broken API wiring
- broken browser-login path
- failing gateway test
- stale top-level product docs
- incomplete agent join expectations

Once those are fixed, the repo will be in a state where parity work can be evaluated honestly.

---

## 13. Source References

### Current repo
- `README.md`
- `spec.md`
- `docs/getting-started.md`
- `docs/self-hosting.md`
- `docs/api.md`
- `docs/architecture.md`
- `cmd/ussycode/main.go`
- `cmd/ussyverse-agent/main.go`
- `internal/api/handler.go`
- `internal/admin/admin.go`
- `internal/ssh/commands.go`
- `internal/ssh/browser.go`
- `internal/proxy/auth.go`
- `internal/proxy/caddy.go`
- `internal/gateway/metadata.go`
- `internal/gateway/llm.go`
- `internal/gateway/email.go`
- `internal/gateway/email_send.go`
- `internal/storage/zfs.go`
- `internal/vm/manager.go`
- `internal/controlplane/nodemanager.go`
- `internal/scheduler/scheduler.go`
- `internal/mesh/wireguard.go`
- `PROGRESS-A.md`
- `PROGRESS-B.md`
- `PROGRESS-C.md`
- `PROGRESS-E.md`
- `PROGRESS-F.md`
- `PROGRESS-G.md`
- `handoff.md`

### exe.dev live command reconnaissance
- `ssh exe.dev help`
- `ssh exe.dev help new`
- `ssh exe.dev help ls`
- `ssh exe.dev help rm`
- `ssh exe.dev help restart`
- `ssh exe.dev help rename`
- `ssh exe.dev help tag`
- `ssh exe.dev help cp`
- `ssh exe.dev help share`
- `ssh exe.dev help share show`
- `ssh exe.dev help share port`
- `ssh exe.dev help share set-public`
- `ssh exe.dev help share set-private`
- `ssh exe.dev help share add`
- `ssh exe.dev help share remove`
- `ssh exe.dev help share add-link`
- `ssh exe.dev help share remove-link`
- `ssh exe.dev help share receive-email`
- `ssh exe.dev help share access`
- `ssh exe.dev help whoami`
- `ssh exe.dev help ssh-key`
- `ssh exe.dev help ssh-key list`
- `ssh exe.dev help ssh-key add`
- `ssh exe.dev help ssh-key remove`
- `ssh exe.dev help ssh-key rename`
- `ssh exe.dev help shelley`
- `ssh exe.dev help shelley install`
- `ssh exe.dev help shelley prompt`
- `ssh exe.dev help browser`
- `ssh exe.dev help ssh`

### exe.dev docs crawled / fetched
- `https://exe.dev/`
- `https://exe.dev/docs/what-is-exe`
- `https://exe.dev/docs/pricing`
- `https://exe.dev/docs/proxy`
- `https://exe.dev/docs/sharing`
- `https://exe.dev/docs/cnames`
- `https://exe.dev/docs/login-with-exe`
- `https://exe.dev/docs/api`
- `https://exe.dev/docs/https-api`
- `https://exe.dev/docs/receive-email`
- `https://exe.dev/docs/send-email`
- `https://exe.dev/docs/shelley/intro`
- `https://exe.dev/docs/shelley/byok`
- `https://exe.dev/docs/shelley/llm-gateway`
- `https://exe.dev/docs/shelley/agents-md`
- `https://exe.dev/docs/shelley/upgrading`
- `https://exe.dev/docs/faq/how-exedev-works`
- `https://exe.dev/docs/faq/cross-vm-networking`
- `https://exe.dev/docs/use-case-openclaw`
- `https://exe.dev/docs/use-case-agent`
- `https://exe.dev/docs/use-case-gh-action-runner`
- `https://exe.dev/docs/use-case-marimo`
- `https://exe.dev/docs/guts`
- `https://exe.dev/docs/why-exe`

---

## 14. Final Recommendation

Do **not** try to "finish the whole spec" blindly.

Instead, use exe.dev as a product benchmark and execute in this order:

1. **stabilize truth**
2. **ship the SSH → VM → HTTPS → share → API loop**
3. **ship browser auth + identity headers + signed token parity**
4. **ship email + agent-native image quality**
5. **then decide how much hosted/team/cluster parity really matters**

That path gets `ussycode` closer to feeling like exe.dev much faster than finishing every architectural ambition in parallel.
