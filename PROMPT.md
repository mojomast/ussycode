# USSYCODE ONE-SHOT DEVELOPMENT PROMPT

> Give this entire prompt to an orchestrator agent that can spin up subagents.
> It will execute the full spec autonomously with no further human input needed.

---

## MISSION

You are the orchestrator for building **ussycode** -- a self-hosted dev environment platform for the Ussyverse. The full product specification is in `spec.md` at `/home/mojo/projects/battlebussy2/exedevussy/spec.md`. Read it completely before doing anything else.

There is an **existing codebase** that is substantially built (Phases 1-2 complete). Your job is to:
1. Rename the project from `exedevussy` to `ussycode`
2. Complete all remaining features across 7 parallel development tracks
3. Ensure everything compiles and tests pass
4. Track all progress via the circular development protocol

## EXISTING CODEBASE STATE

The project is at `/home/mojo/projects/battlebussy2/exedevussy/`. It is a Go project (module `github.com/mojomast/exedevussy`, Go 1.25) with the following WORKING components:

- **SSH Gateway** (`internal/ssh/`): Custom SSH server via gliderlabs/ssh, user registration, interactive REPL shell, 16 commands (help, whoami, ls, new, ssh, stop, restart, start, cp, rm, tag, rename, ssh-key, share)
- **Database** (`internal/db/`): SQLite with WAL mode, split reader/writer, 30+ query methods, goose migrations, full CRUD for users/VMs/keys/shares/tokens. TESTED.
- **Auth** (`internal/auth/`): SSH key-based stateless tokens, HTTP Bearer middleware. TESTED.
- **VM Manager** (`internal/vm/`): Firecracker SDK integration, OCI image pull + rootfs extraction, TAP/bridge networking. IMPLEMENTED but requires Firecracker + root to actually run.
- **Proxy** (`internal/proxy/`): Caddy admin API integration, forward-auth proxy with token verification. IMPLEMENTED.
- **Metadata Service** (`internal/gateway/`): AWS-style 169.254.169.254 service. VM info, SSH keys, hostname, env vars work. **LLM proxy and email are STUBS.**
- **Base Image** (`images/ussyuntu/`): Ubuntu 24.04 Dockerfile with Go, Python, Node, systemd, init scripts.
- **Config** (`internal/config/`): Config struct from env vars. **EXISTS but NOT wired into main.go** (main.go uses CLI flags directly).

**What does NOT exist yet:** Agent binary, gRPC protos, WireGuard mesh, scheduler, tutorial command, browser command, doc command, HTTPS API, admin panel, trust levels, custom domains, LLM proxy implementation, email implementation, deployment scripts, project templates, BattleBussy integration.

## EXECUTION PLAN

You MUST spin up subagents to work on the 7 tracks defined in `spec.md` Section 10-11. Tracks A through E have NO dependencies on each other and MUST be launched in parallel. Track F depends on A+B+C. Track G depends on all.

### PHASE 1: Launch Tracks A-E in parallel (5 subagents)

**IMPORTANT: Before launching any track, read spec.md Section 11 for that track's full specification.**

#### Subagent 1: TRACK A - Core Hardening
```
You are working on Track A of the ussycode project. Read spec.md at /home/mojo/projects/battlebussy2/exedevussy/spec.md, specifically Section 11 "TRACK A: Core Hardening".

Your tasks in order:
A.1 - Rename exedevussy -> ussycode (module path, directory, imports, user-facing strings, env vars, base image references)
A.2 - Wire internal/config/config.go into cmd/ussycode/main.go (replace CLI flags with config package)
A.3 - Implement internal/storage/zfs.go (ZFS zvol management: clone, resize, destroy, quota, snapshot)
A.4 - Migrate internal/vm/network.go from iptables to nftables (firecracker table, per-VM rules, metadata interception, nftables sets, reconciler)
A.5 - Enhance testing (integration tests for VM lifecycle, proxy routes)

After EACH task:
1. Run: go build ./... && go test ./...
2. Fix any failures before moving to the next task
3. Update PROGRESS-A.md at the project root with completed/in-progress/next items
4. Commit with message format: "track-a: <description>"

If you hit a blocker, document it in PROGRESS-A.md and move to the next task.

Interface contracts you must maintain (other tracks depend on these):
- StorageBackend interface in internal/storage/ (CloneForVM, DestroyVM, ResizeVM, GetUsage)
- NetworkManager interface in internal/vm/ (SetupVM, CleanupVM, GuestBootArgs)

The rename (A.1) is the FIRST thing you must do. Every other track depends on the module being github.com/mojomast/ussycode.
```

#### Subagent 2: TRACK B - Ussyverse Server Pool
```
You are working on Track B of the ussycode project. Read spec.md at /home/mojo/projects/battlebussy2/exedevussy/spec.md, specifically Section 11 "TRACK B: Ussyverse Server Pool (Agent)".

IMPORTANT: Track A is renaming the module concurrently. Start by reading the current go.mod to understand the module path. If it's still exedevussy, use that for now and note in your PROGRESS file that a rebase onto the renamed module will be needed.

Your tasks in order:
B.1 - Create proto/ directory with gRPC Protocol Buffer definitions (NodeService, VMService, SchedulerService)
B.2 - Create cmd/ussyverse-agent/ binary (join, status, version subcommands)
B.3 - Implement mTLS certificate management (CA in control plane, 24h agent certs, join tokens)
B.4 - Implement heartbeat & health (bidirectional gRPC stream, NodeStatus, lease model, timeouts)
B.5 - Implement WireGuard mesh (embed wireguard-go, /24 per node from 100.64.0.0/10, magicsock NAT traversal)
B.6 - Implement VM placement scheduler (internal/scheduler/, filter+score, bin packing)
B.7 - Create agent installer script (deploy/install-agent.sh)

After EACH task:
1. Run: go build ./... && go test ./...
2. Fix any failures before moving to the next task
3. Update PROGRESS-B.md at the project root
4. Commit with message format: "track-b: <description>"

Interface contracts you must provide (other tracks will consume):
- Scheduler interface: PlaceVM, DrainNode, ListNodes
- gRPC service definitions in proto/

For WireGuard (B.5), use these Go packages:
- golang.zx2c4.com/wireguard (userspace WireGuard)
- golang.zx2c4.com/wireguard/wgctrl (device control)
- tailscale.com/derp (DERP relay, MIT licensed)
- tailscale.com/wgengine/magicsock (NAT traversal)

For gRPC: use google.golang.org/grpc and google.golang.org/protobuf. Generate Go code with protoc-gen-go and protoc-gen-go-grpc.
```

#### Subagent 3: TRACK C - UX & Onboarding
```
You are working on Track C of the ussycode project. Read spec.md at /home/mojo/projects/battlebussy2/exedevussy/spec.md, specifically Section 11 "TRACK C: UX & Onboarding".

IMPORTANT: Track A is renaming the module concurrently. Work with whatever module name currently exists.

Your tasks in order:
C.1 - Implement tutorial command (internal/ssh/tutorial.go, 10 progressive lessons, DB progress tracking, resume support)
C.2 - Implement browser command (magic link token, 5min expiry, QR code generation, HTTP handler for magic auth)
C.3 - Implement doc command (list topics, render markdown docs to terminal, basic formatting)
C.4 - Implement project templates (templates/ directory structure, template.json metadata, new --template=<name> integration)
C.5 - Customize welcome messages (VM count, tips for new users, ussyverse branding)

After EACH task:
1. Run: go build ./... && go test ./...
2. Fix any failures before moving to the next task
3. Update PROGRESS-C.md at the project root
4. Commit with message format: "track-c: <description>"

For the tutorial (C.1), add a migration file internal/db/migrations/002_tutorial_progress.sql with a tutorial_progress table (user_id, lesson_number, completed_at). Add query methods to internal/db/queries.go.

For QR codes (C.2), use a pure-Go QR library like github.com/skip2/go-qrcode or render ASCII QR in terminal.

Register all new commands in internal/ssh/commands.go's command map.
```

#### Subagent 4: TRACK D - Gateway Services
```
You are working on Track D of the ussycode project. Read spec.md at /home/mojo/projects/battlebussy2/exedevussy/spec.md, specifically Section 11 "TRACK D: Gateway Services".

IMPORTANT: Track A is renaming the module concurrently. Work with whatever module name currently exists.

Your tasks in order:
D.1 - Implement LLM Gateway proxy (internal/gateway/llm.go, reverse proxy to configurable backends, BYOK key injection, rate limiting, usage tracking)
D.2 - Implement email receive (SMTP listener or Postfix integration, Maildir delivery, auto-disable on backlog)
D.3 - Implement email send (POST handler in metadata service, owner-only validation, SMTP relay, rate limiting)

After EACH task:
1. Run: go build ./... && go test ./...
2. Fix any failures before moving to the next task
3. Update PROGRESS-D.md at the project root
4. Commit with message format: "track-d: <description>"

The current stubs are in internal/gateway/metadata.go around lines 280-327. Replace them with real implementations.

For LLM proxy: create internal/gateway/llm.go. Use net/http/httputil.ReverseProxy. Support these provider paths:
- /gateway/llm/anthropic -> configurable Anthropic API URL
- /gateway/llm/openai -> configurable OpenAI API URL
- /gateway/llm/ollama -> configurable Ollama URL (self-hosted)
- /gateway/llm/fireworks -> configurable Fireworks URL

For BYOK: add a new SSH command "llm-key set <provider> <key>" that stores encrypted API keys in the DB. Add a migration for the llm_keys table. When proxying, inject the user's key as Authorization header.

For email: consider using github.com/emersion/go-smtp for the SMTP server.

Interface contract: LLMGateway interface with Proxy() and SetUserKey() methods.
```

#### Subagent 5: TRACK E - API & Admin
```
You are working on Track E of the ussycode project. Read spec.md at /home/mojo/projects/battlebussy2/exedevussy/spec.md, specifically Section 11 "TRACK E: API & Admin".

IMPORTANT: Track A is renaming the module concurrently. Work with whatever module name currently exists.

Your tasks in order:
E.1 - Implement HTTPS API (internal/api/handler.go, POST /exec, Bearer token auth with usy0/usy1 token format, rate limiting, error codes)
E.2 - Implement admin web panel (internal/admin/, embedded HTML templates, dashboard/users/VMs/nodes pages, /admin/api/ JSON endpoints)
E.3 - Implement trust levels & quotas (DB migration for trust_level column, enforce limits in new/ssh-key commands, admin CLI for setting trust)
E.4 - Implement custom domains (share cname command, custom_domains DB table, Caddy route management, domain ownership validation)

After EACH task:
1. Run: go build ./... && go test ./...
2. Fix any failures before moving to the next task
3. Update PROGRESS-E.md at the project root
4. Commit with message format: "track-e: <description>"

For the HTTPS API (E.1): The token format is usy0.<base64url_permissions_json>.<base64url_ssh_signature>. Permissions JSON supports: exp, nbf, cmds (allowed commands), ctx (opaque passthrough). Verify signatures using the existing internal/auth package. Add POST /exec handler to main.go's HTTP server.

For admin panel (E.2): Use Go html/template with embedded templates via go:embed. Minimal CSS, no JS frameworks. Auth via existing token system (require admin trust level). Serve at /admin/ path.

For trust levels (E.3): Add migration 003_trust_levels.sql. Default trust is "newbie". Limits defined in spec.md Section 7.4.
```

### PHASE 2: Launch Track F (after A+B+C signal completion)

Wait until PROGRESS-A.md, PROGRESS-B.md, and PROGRESS-C.md all show "Status: COMPLETE" or substantial completion. Then launch:

#### Subagent 6: TRACK F - Ussyverse Integration
```
You are working on Track F of the ussycode project. Read spec.md at /home/mojo/projects/battlebussy2/exedevussy/spec.md, specifically Section 11 "TRACK F: Ussyverse Integration".

Tracks A, B, and C should be complete. Read PROGRESS-A.md, PROGRESS-B.md, PROGRESS-C.md to understand what's available.

Your tasks:
F.1 - BattleBussy Arena (arena command set, VM provisioning for matches, WebSocket scoring, teardown, ELO ranking)
F.2 - Pre-configured agent templates (geoffrussy, battlebussy-agent, openclawssy templates in templates/)
F.3 - Ussyverse branding & community (welcome messages, community command, README, LICENSE with credits)

After EACH task: build, test, update PROGRESS-F.md, commit.
```

### PHASE 3: Launch Track G (after all tracks signal completion)

#### Subagent 7: TRACK G - Deployment & Ops
```
You are working on Track G of the ussycode project. Read spec.md at /home/mojo/projects/battlebussy2/exedevussy/spec.md, specifically Section 11 "TRACK G: Deployment & Ops".

All other tracks should be complete. Read all PROGRESS-*.md files.

Your tasks:
G.1 - Ansible playbooks (deploy/ansible/)
G.2 - Agent installer script (deploy/install-agent.sh)
G.3 - Control plane installer script (deploy/install-control.sh)
G.4 - Documentation (docs/ directory)

After EACH task: update PROGRESS-G.md, commit.
```

## CRITICAL RULES FOR ALL SUBAGENTS

1. **READ spec.md FIRST.** It contains all technical details, interface contracts, and requirements.
2. **Build and test after every task.** `go build ./... && go test ./...` must pass.
3. **Update PROGRESS files.** This is how the orchestrator and other tracks know your state.
4. **Commit after every task.** Use the format: `track-X: description`
5. **Don't break other tracks.** If you need to modify shared files (commands.go, main.go, queries.go), be careful of conflicts.
6. **Document blockers.** If something can't be done without another track, note it and move on.
7. **Interface contracts are sacred.** The interfaces defined in spec.md Section 12.3 are the contract between tracks. Implement them exactly.
8. **No external services required for compilation.** Code must compile without Firecracker, ZFS, Caddy, or any external service running. Use build tags for integration tests.

## ORCHESTRATOR RESPONSIBILITIES

As the orchestrator, you must:
1. Launch Tracks A-E simultaneously as 5 parallel subagents
2. Monitor PROGRESS files periodically
3. Resolve conflicts between tracks (especially in shared files)
4. Launch Track F when A+B+C are substantially complete
5. Launch Track G when all tracks are complete
6. Run final integration: `go build ./... && go test ./... -count=1 -race`
7. Verify all PROGRESS files show COMPLETE
8. Create a final summary commit: "ussycode v1.0: all tracks complete"

## FINAL VERIFICATION CHECKLIST

Before declaring done:
- [ ] Module path is github.com/mojomast/ussycode
- [ ] Binary name is ussycode (cmd/ussycode/)
- [ ] Agent binary is ussyverse-agent (cmd/ussyverse-agent/)
- [ ] `go build ./...` succeeds with zero errors
- [ ] `go test ./...` passes with zero failures
- [ ] `go vet ./...` reports no issues
- [ ] All 7 PROGRESS files show COMPLETE
- [ ] spec.md credits Kyle Durepos and shuv as co-creators
- [ ] LICENSE file is MIT
- [ ] README.md references the Ussyverse and ussy.host

---

*This prompt is self-contained. The agent receiving it needs no further human input to execute the full development plan. All technical details, interface contracts, dependency versions, and architectural decisions are in spec.md.*
