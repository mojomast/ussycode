# ussycode Integration Audit Report

**Module:** `github.com/mojomast/ussycode`  
**Date:** 2026-03-20  
**Codebase:** 62 `.go` files, **20,442 lines** of Go  
**Tracks Audited:** A (Core Hardening), B (Server Pool), C (Community/Arena), E (API & Admin), F (Agent/Mesh)

---

## 1. Build Check

**Command:** `go build ./...`  
**Result:** ✅ **PASSES** — zero errors

All 18 packages compile cleanly. No unresolved symbols, type errors, or missing dependencies.

---

## 2. Test Check

**Command:** `go test ./... -count=1`  
**Result:** ✅ **ALL 12 TEST SUITES PASS**

| Package | Status | Notes |
|---------|--------|-------|
| `internal/admin` | ✅ PASS | 0.387s |
| `internal/api` | ✅ PASS | 0.250s |
| `internal/auth` | ✅ PASS | 0.010s |
| `internal/config` | ✅ PASS | 0.005s |
| `internal/db` | ✅ PASS | 0.210s (17 tests: migration, quota, custom domain) |
| `internal/gateway` | ✅ PASS | 0.581s |
| `internal/pki` | ✅ PASS | 0.015s |
| `internal/scheduler` | ✅ PASS | 0.010s |
| `internal/ssh` | ✅ PASS | 1.457s |
| `internal/storage` | ✅ PASS | 0.003s |
| `internal/vm` | ✅ PASS | 0.007s |
| `cmd/ussycode` | ⚠️ No test files |
| `cmd/ussyverse-agent` | ⚠️ No test files |
| `internal/agent` | ⚠️ No test files |
| `internal/controlplane` | ⚠️ No test files |
| `internal/mesh` | ⚠️ No test files |
| `internal/proto/nodev1` | ⚠️ No test files |
| `internal/proxy` | ⚠️ No test files |

**7 packages have no test files.** While some of these are small (proto definitions, main entrypoints), `internal/agent`, `internal/controlplane`, `internal/mesh`, and `internal/proxy` contain significant logic that is untested.

---

## 3. Vet Check

**Command:** `go vet ./...`  
**Result:** ✅ **PASSES** — zero issues

No shadowed variables, Printf mismatches, unreachable code, or other vet findings.

---

## 4. Import Consistency

**Check:** All `.go` files must use `github.com/mojomast/ussycode` (not `exedevussy`)  
**Result:** ✅ **CLEAN** — zero matches for `exedevussy` in any `.go` file

The module rename from Track A.1 was completed correctly across all files.

---

## 5. Migration Numbering

**Directory:** `internal/db/migrations/`  
**Result:** ✅ **Sequential, no gaps or conflicts**

| # | File | Purpose |
|---|------|---------|
| 001 | `001_initial_schema.sql` | Users, VMs, SSH keys |
| 002 | `002_vm_networking.sql` | TAP/IP/MAC columns |
| 003 | `003_ssh_key_mgmt.sql` | SSH key management |
| 004 | `004_arena_tables.sql` | Arena matches, participants, ELO |
| 005 | `005_tutorial_progress.sql` | Tutorial progress tracking |
| 006 | `006_api_tokens.sql` | API tokens (usy1.) |
| 007 | `007_admin_audit_log.sql` | Admin audit logging |
| 008 | `008_user_quotas.sql` | Trust levels & quota columns |
| 009 | `009_custom_domains.sql` | Custom domain records |

9 migrations, numbered 001–009 with no gaps, no conflicting prefixes.

---

## 6. Shared File Inconsistencies

### 6a. `commands.go` (1,734 lines)

- **Static commands map** (line 26): 15 commands registered statically (`help`, `whoami`, `ls`, `new`, `rm`, `ssh`, `stop`, `restart`, `tag`, `rename`, `cp`, `start`, `ssh-key`, `share`, `admin`)
- **Dynamic registrations via `init()`**: 4 commands registered at runtime (`community`, `arena`, `browser`, `tutorial`)
- **Total registered:** 19 commands

🔴 **ISSUE: `cmdLLMKey` is defined (lines 1561–1692, ~131 lines) but NEVER registered.** Neither in the static map nor via `RegisterCommand()` in any `init()`. This means the `llm-key` command is completely unreachable. 6 functions are dead: `cmdLLMKey`, `cmdLLMKeyHelp`, `cmdLLMKeySet`, `cmdLLMKeyList`, `cmdLLMKeyRemove`, plus helper logic. The `share` help text references LLM functionality but `llm-key` is not callable.

### 6b. `queries.go` (1,372 lines)

🟡 **ISSUE: Duplicate scan helper interfaces.** Lines 1041–1058 define two interfaces with identical signatures:
  - `scannable` (line 1043): `Scan(dest ...any) error` — **used** by `scanVM()`
  - `rowScanner` (line 1056): `Scan(dest ...any) error` — **UNUSED**, dead code
  - `scanVMRow()` (line 1060) uses an inline anonymous interface `interface{ Scan(dest ...any) error }` instead of either named interface

🟡 **ISSUE: Duplicate `--- scan helpers ---` comment blocks** at lines 868 and 1041, suggesting a merge/copy artifact between tracks.

### 6c. `models.go` (271 lines)

No inconsistencies found. All models are well-structured and used. `TrustLimits` map is consistent with migration 008. `CustomDomain` model matches migration 009.

### 6d. `main.go` (173 lines)

No inconsistencies found. Clean startup flow: config → DB → migrations → server.

---

## 7. Interface Contracts

### ✅ `StorageBackend` — `internal/storage/zfs.go` line 20
Well-formed interface with 4 methods:
- `CloneForVM(ctx, baseImage, vmID) (devicePath, error)`
- `DestroyVM(ctx, vmID) error`
- `ResizeVM(ctx, vmID, newSize) error`
- `GetUsage(ctx, userID) (*UsageStats, error)`

All methods have context parameters, clear documentation, and a concrete implementation (`ZFSBackend`) that satisfies it.

### 🟡 `NetworkManager` — `internal/vm/network.go` line 16
**This is a concrete struct, NOT an interface.** It has fields (`bridge`, `subnet`, `gateway`, `allocated`, `nextIP`, `mu`, `logger`, `firewall`) and methods, but no interface contract. There IS a `FirewallManager` interface in `internal/vm/nftables.go` which `NetworkManager` depends on, but `NetworkManager` itself is not abstracted behind an interface. This limits testability and pluggability.

### ✅ `Scheduler` — `internal/scheduler/scheduler.go` line 88
Well-formed interface with 3 methods:
- `PlaceVM(ctx, spec) (*NodeStatus, error)`
- `DrainNode(ctx, nodeID) error`
- `ListNodes(ctx) ([]*NodeStatus, error)`

Also includes a companion `NodeProvider` interface (line 101) for dependency injection. Clean design.

### ✅ `LLMGateway` — `internal/gateway/llm.go` line 20
Well-formed interface with 3 methods:
- `Proxy(w, r, provider)`
- `SetUserKey(ctx, userID, provider, key) error`
- `GetUserKey(ctx, userID, provider) (string, error)`

Has a concrete `Gateway` implementation. Well-documented.

**Summary:** 3 of 4 interfaces are well-formed. `NetworkManager` is a struct, not an interface.

---

## 8. Dead Code & Stubs

### 🔴 Dead Code — Unreachable

| Item | Location | Lines | Severity |
|------|----------|-------|----------|
| `cmdLLMKey` + 5 helpers | `commands.go:1561–1692` | ~131 | HIGH — unreachable command, never registered |
| `rowScanner` interface | `queries.go:1056–1058` | 3 | LOW — defined but never referenced |

### 🟡 Stubs / Incomplete Implementations (TODOs)

| TODO | Location | Description |
|------|----------|-------------|
| `// TODO: Check via netlink or ip link show` | `vm/integration_test.go:41` | TAP verification incomplete |
| `// TODO: Parse nft list table inet ussycode` | `vm/integration_test.go:81` | Firewall rule verification incomplete |
| `// TODO: Set up full VM manager with real database and firecracker` | `vm/integration_test.go:111` | Full integration test not implemented |
| `// TODO: dispatch to VM manager` | `agent/heartbeat.go:179` | Agent heartbeat doesn't dispatch to VM manager yet |

### 🟡 Redundant Functions

| Item | Location | Notes |
|------|----------|-------|
| `hasFlag()` | `commands.go:823` | Returns `(bool, []string)` with remaining args |
| `hasArgFlag()` | `tutorial.go:397` | Returns just `bool` — simpler version of `hasFlag` for the same purpose. Two functions with overlapping intent. |

---

## 9. Cross-Track Integration Issues

### 🔴 Admin Trust Level / Quota Inconsistency

**admin.go line 680:** `(h *Handler) setUserTrustLevel()` — updates ONLY `trust_level` column  
**queries.go line 755:** `(d *DB) SetUserTrustLevel()` — updates `trust_level` AND resets all 4 quota columns (`vm_limit`, `cpu_limit`, `ram_limit_mb`, `disk_limit_mb`)

The admin **web panel** (Track E.2) uses its own local method instead of the centralized DB method (Track E.3). **Consequence:** When an admin changes a user's trust level via the **web panel**, the user's quota limits are NOT updated to match the new level — the user keeps their old quotas. However, the SSH `admin set-trust` command (`commands.go` line 1728) correctly calls `s.gw.DB.SetUserTrustLevel()` which DOES update quotas. So the same operation produces different results depending on whether it's done via the web UI or the SSH CLI.

### 🟡 `go.mod` Specifies Non-Existent Go Version

```
go 1.25.7
```

Go versions are currently at 1.22/1.23 (as of early 2026). Version 1.25.7 does not exist. The toolchain gracefully ignores this, but it's incorrect and could cause issues with stricter tooling or CI systems.

### 🟡 `hasArgFlag` vs `hasFlag` Cross-File Dependency

`browser.go` (line 22) calls `hasArgFlag()` which is defined in `tutorial.go` (line 397). This works because they're in the same `ssh` package, but it's a hidden coupling — if `tutorial.go` were ever removed or moved, `browser.go` would break with no obvious reason. Similarly, `arena.go` calls `hasFlag()` from `commands.go`.

---

## 10. Package Inventory & Test Coverage

| # | Package | Files | Has Tests | Notes |
|---|---------|-------|-----------|-------|
| 1 | `cmd/ussycode` | 1 | ❌ | Server entrypoint |
| 2 | `cmd/ussyverse-agent` | 1 | ❌ | Agent binary entrypoint |
| 3 | `internal/admin` | 3 | ✅ | Web admin panel |
| 4 | `internal/agent` | 2 | ❌ | Agent heartbeat/handler — has TODO stub |
| 5 | `internal/api` | 3 | ✅ | HTTPS API + rate limiting |
| 6 | `internal/auth` | 1+ | ✅ | SSH auth |
| 7 | `internal/config` | 1+ | ✅ | Config loading |
| 8 | `internal/controlplane` | 1+ | ❌ | Control plane logic |
| 9 | `internal/db` | 5+ | ✅ | Database, models, migrations, queries |
| 10 | `internal/gateway` | 5+ | ✅ | SSH gateway, LLM, email, crypto |
| 11 | `internal/mesh` | 1+ | ❌ | Mesh networking |
| 12 | `internal/pki` | 1+ | ✅ | PKI/certificate management |
| 13 | `internal/proto/nodev1` | 1+ | ❌ | Protobuf definitions |
| 14 | `internal/proxy` | 1+ | ❌ | Caddy proxy integration |
| 15 | `internal/scheduler` | 2+ | ✅ | VM scheduling |
| 16 | `internal/ssh` | 8+ | ✅ | Shell, commands, arena, tutorial, browser |
| 17 | `internal/storage` | 2+ | ✅ | ZFS storage backend |
| 18 | `internal/vm` | 3+ | ✅ | VM management, networking, firewall |

**18 packages total. 11 have tests, 7 do not.**

---

## Summary of Findings

### 🔴 High Severity (2)

1. **`cmdLLMKey` dead code** — 131 lines of unreachable code. The `llm-key` command is defined but never registered. Should either be registered or removed.

2. **Admin web panel trust level doesn't update quotas** — `admin.go` uses a local method that skips quota updates, while `queries.go` has the correct version. The SSH `admin set-trust` command correctly uses the DB method, so the same operation has different behavior depending on the interface (web vs CLI).

### 🟡 Medium Severity (5)

3. **`go.mod` specifies `go 1.25.7`** — non-existent Go version. Should be a real version (e.g., `1.22` or `1.23`).

4. **`NetworkManager` is a struct, not an interface** — limits testability and doesn't match the pattern of the other 3 contracts (`StorageBackend`, `Scheduler`, `LLMGateway`).

5. **7 packages have no tests** — including `internal/agent`, `internal/controlplane`, `internal/mesh`, and `internal/proxy` which contain non-trivial logic.

6. **Duplicate/redundant scan helpers in `queries.go`** — `rowScanner` interface is unused, `scanVMRow` uses an inline interface, duplicate section comment headers.

7. **`hasArgFlag` / `hasFlag` duplication** — two functions with overlapping purpose in different files, creating hidden cross-file coupling.

### ℹ️ Low Severity (2)

8. **4 TODO stubs** — 3 in VM integration tests (incomplete assertions), 1 in agent heartbeat (unfinished dispatch).

9. **`internal/admin/embed.go` pre-existing `//go:embed` issue** — empty `web/templates` dir. Known issue, not introduced by any track work.

---

*End of audit report.*
