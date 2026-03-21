# Handoff — ussycode

_Current as of 2026-03-21. Read this before touching any code._

## Completed: Firecracker Jailer Integration (Phase 6 — hardenussy branch)

### What was done

Added optional Firecracker jailer support, controlled entirely by config. When `JailerBin` is set, VMs run inside chroot isolation via the jailer binary. When empty (default), behavior is unchanged.

### Files Modified

1. **`internal/config/config.go`** — Added 4 new config fields:
   - `JailerBin` (env: `USSYCODE_JAILER_BIN`, flag: `-jailer`, default: `""` = disabled)
   - `JailerUID` (env: `USSYCODE_JAILER_UID`, flag: `-jailer-uid`, default: `30000`)
   - `JailerGID` (env: `USSYCODE_JAILER_GID`, flag: `-jailer-gid`, default: `30000`)
   - `ChrootBaseDir` (env: `USSYCODE_CHROOT_BASE_DIR`, flag: `-chroot-base`, default: `/srv/jailer`)

2. **`internal/vm/manager.go`** — Added jailer fields to `ManagerConfig` struct. Updated `NewManager()` to pass `JailerConfig` to `NewFirecrackerBackend()`.

3. **`cmd/ussycode/main.go`** — Updated `ManagerConfig` initialization to pass all 4 jailer config fields from the global config.

4. **`internal/vm/firecracker.go`** — Major changes:
   - Added `JailerConfig` struct with `Enabled()` method
   - Added `jailer *JailerConfig` field to `FirecrackerBackend`
   - Added `jailedID string` field to `FirecrackerVM` (for chroot cleanup)
   - Updated `NewFirecrackerBackend()` signature to accept `*JailerConfig`
   - Added jailer binary validation (gracefully disables if binary not found)
   - Added chroot base dir creation on startup
   - Updated `StartVM()` to branch: if jailer enabled, sets `fcCfg.JailerCfg` with `NaiveChrootStrategy`, cgroup v2, `Daemonize: false`; if disabled, uses `WithProcessRunner()` as before
   - Added jailer chroot cleanup in `StopVM()` (removes `{ChrootBaseDir}/firecracker/{vmID}/`)

### Key Design Decisions

- **Jailer is optional** — empty `JailerBin` = disabled, no behavior change
- **Graceful degradation** — if jailer binary path is set but binary not found, logs warning and disables jailer instead of failing
- **NaiveChrootStrategy** — hard-links kernel/drives into chroot (requires same filesystem)
- **CgroupVersion: "2"** — system uses cgroup v2
- **Daemonize: false** — required for Go process management to work
- **No `WithProcessRunner()`** when jailer enabled — SDK builds the command internally

### Context for Next Task

- The existing Phase 0 blockers from the previous handoff still apply
- All LSP errors in the repo are false positives from missing `go mod download`
- The `NetworkConfig` "undefined" LSP error in firecracker.go is because the type is defined in a different file in the same package

---

## Previous Context (preserved from earlier handoff)

See git history for the full original handoff content. Key points:
- 80+ tests across 12 packages
- 1 known test failure: `TestSMTPServer_DotStuffing` in `internal/gateway`
- Phase 0 blockers (P0-1 through P0-8) still need resolution before feature work
- `go build ./...` should remain clean
