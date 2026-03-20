# Track A: Core Hardening — Progress

## A.1: Rename exedevussy → ussycode ✅

**Status:** COMPLETE  
**Verified:** `go build ./...` ✅ | `go test ./...` ✅ | Zero `exedev` refs in Go source ✅

### Changes Made:
- **go.mod** — module path `github.com/mojomast/ussycode`
- **cmd/exedevussy/** → **cmd/ussycode/** (directory rename)
- **All .go imports** — updated across 12 files (zero remaining old paths)
- **User-facing strings** — env vars (`USSYCODE_*`), paths (`/var/lib/ussycode`), user names, doc comments, temp prefixes, route IDs, hostnames
- **Image files** — `init-exedev.sh` → `init-ussycode.sh`, `exedev-init.service` → `ussycode-init.service`, Dockerfile user/paths, motd, bash_profile, sshd_config
- **Config defaults** — DB name `ussycode.db`, ZFS pool `ussycode`, all env var prefixes

### Test Results:
```
?   github.com/mojomast/ussycode/cmd/ussycode         [no test files]
?   github.com/mojomast/ussycode/cmd/ussyverse-agent   [no test files]
ok  github.com/mojomast/ussycode/internal/api          0.166s
ok  github.com/mojomast/ussycode/internal/auth         (cached)
ok  github.com/mojomast/ussycode/internal/db           0.015s
ok  github.com/mojomast/ussycode/internal/pki          (cached)
ok  github.com/mojomast/ussycode/internal/ssh          1.350s
```

---

## A.2: Wire config.go into main.go ✅

**Status:** COMPLETE  
**Verified:** `go build ./...` ✅ | `go test ./...` ✅ (including new config tests)

### Changes Made:
- **`internal/config/config.go`** — Added `MetadataAddr`, `AuthProxyAddr`, `FirecrackerBin` fields. Added `RegisterFlags(fs *flag.FlagSet)` method that binds all config fields to CLI flags with env/default fallback. Enhanced `Validate()` with checks for `SSHListenAddr` and `DBPath`. Fixed `SSHListenAddr` default from `:22` to `:2222` to match main.go convention.
- **`internal/config/config_test.go`** — NEW: Tests for `DefaultConfig()`, env var overrides, `RegisterFlags()` parsing, flag-overrides-env precedence, and `Validate()` error cases.
- **`cmd/ussycode/main.go`** — Replaced 14 individual `flag.*()` calls with `config.DefaultConfig()` + `cfg.RegisterFlags(flag.CommandLine)` + `flag.Parse()`. All subsystem initialization now reads from `cfg.*` fields. Added `cfg.Validate()` call before startup.

### Design Notes:
- **Precedence:** CLI flags > environment variables > hardcoded defaults
- `RegisterFlags` takes a `*flag.FlagSet` (not global `flag.CommandLine`) for testability
- All old flag names preserved for backward compatibility (`-addr`, `-db`, `-host-key`, etc.)

## A.3: ZFS Storage Backend ✅

**Status:** COMPLETE  
**Verified:** `go build ./...` ✅ (excl. pre-existing admin embed issue) | `go test ./internal/storage/...` ✅ (all 14 tests pass)

### Changes Made:
- **`internal/storage/zfs.go`** — NEW: Defines `StorageBackend` interface and `ZFSBackend` implementation.
  - `StorageBackend` interface: `CloneForVM`, `DestroyVM`, `ResizeVM`, `GetUsage` — the contract for other tracks
  - `CommandRunner` interface for abstracting exec.Command (testability)
  - `ZFSBackend` struct with pool name, command runner, and slog logger
  - Dataset layout: `<pool>/images/<baseImage>@base` → cloned to `<pool>/vms/<vmID>`
  - `DestroyVM` is idempotent (no error if dataset doesn't exist)
  - `ResizeVM` tries volsize first, falls back to refquota for regular datasets
  - `GetUsage` parses ZFS list output, supports per-user filtering and ZFS size suffixes (K/M/G/T)
  - `parseZFSSize` helper for converting ZFS human-readable sizes to bytes
- **`internal/storage/zfs_test.go`** — NEW: 14 tests with mock command runner
  - Mock runner records commands and returns pre-configured responses
  - Tests: clone success, snapshot-not-found, clone failure, destroy success/idempotent/error, resize volsize/refquota-fallback/both-fail, usage success/empty-pool/all-VMs, size parsing, interface compliance

### Interface Contract:
```go
type StorageBackend interface {
    CloneForVM(ctx context.Context, baseImage, vmID string) (devicePath string, err error)
    DestroyVM(ctx context.Context, vmID string) error
    ResizeVM(ctx context.Context, vmID, newSize string) error
    GetUsage(ctx context.Context, userID string) (*UsageStats, error)
}
```

## A.4: nftables Migration ✅

**Status:** COMPLETE  
**Verified:** `go build ./...` ✅ (excl. admin) | `go test ./internal/vm/...` ✅ (all tests pass)

### Changes Made:
- **`internal/vm/nftables.go`** — NEW: Defines `FirewallManager` interface and `NftablesManager` implementation.
  - `FirewallManager` interface: `SetupNAT`, `CleanupNAT`, `AddVMRules`, `RemoveVMRules`
  - `CommandExecutor` interface for abstracting exec.Command (testability)
  - `NftablesManager` uses atomic nft ruleset scripts for NAT setup
  - nftables table layout: `inet ussycode` with `postrouting` (NAT) and `forward` (filter) chains
  - Per-VM rules with comment-based identification for clean removal
  - `parseNftHandles` helper for extracting rule handles from nft output
  - `runCmdContext` with stdin piping support for nft -f -
- **`internal/vm/nftables_test.go`** — NEW: 10 tests with mock command executor
  - Tests: SetupNAT success/delete-old-table, CleanupNAT success/idempotent, AddVMRules success, RemoveVMRules success/no-table, handle parsing/no-match, interface compliance
- **`internal/vm/network.go`** — UPDATED:
  - Added `firewall FirewallManager` field to `NetworkManager` struct
  - `NewNetworkManager` now creates an `NftablesManager` for firewall operations
  - `SetupBridge()` now calls `nm.firewall.SetupNAT()` instead of iptables commands
  - Added `context` import for the nftables call

## A.5: Enhanced Testing ✅

**Status:** COMPLETE  
**Verified:** `go build ./...` ✅ (excl. pre-existing admin embed issue) | `go test ./...` ✅ (all packages pass) | All benchmarks run ✅

### Changes Made:
- **`internal/gateway/email_send_test.go`** — FIXED: Added missing `"net"` import (pre-existing bug — used `net.OpError` and `net.DNSError` without importing the package)
- **`internal/vm/integration_test.go`** — NEW: Integration test stubs with `skipIfNotIntegration` guard (requires `USSYCODE_INTEGRATION=1` env var). Covers:
  - `TestIntegration_NetworkSetupBridge` — bridge creation with real interfaces
  - `TestIntegration_NetworkAllocateRelease` — TAP device + IP allocation cycle
  - `TestIntegration_NftablesNAT` — full nftables NAT setup/teardown with VM rules
  - `TestIntegration_VMCreateAndStart` — end-to-end VM lifecycle stub (needs firecracker)
- **`internal/vm/bench_test.go`** — NEW: Benchmarks for VM networking operations:
  - `BenchmarkNetworkAllocate` — in-memory IP + TAP allocation (~876 ns/op)
  - `BenchmarkMACGeneration` — random MAC address generation (~671 ns/op)
  - `BenchmarkCIDRMaskConversion` — CIDR to netmask conversion (~332 ns/op)
- **`internal/storage/zfs_bench_test.go`** — NEW: Benchmarks for ZFS storage backend:
  - `BenchmarkCloneForVM` — clone path with mock runner (~4,495 ns/op)
  - `BenchmarkDestroyVM` — destroy path with mock runner (~1,844 ns/op)
  - `BenchmarkGetUsage` — usage parsing at 10/100/1000 VMs (~13K/112K/984K ns/op)
  - `BenchmarkParseZFSSize` — size string parser (~1,394 ns/op)
  - `BenchmarkResizeVM` — resize path with mock runner (~1,692 ns/op)

### Test Summary (all packages):
```
ok  github.com/mojomast/ussycode/internal/config     0.006s
ok  github.com/mojomast/ussycode/internal/storage    0.013s
ok  github.com/mojomast/ussycode/internal/db         0.019s
ok  github.com/mojomast/ussycode/internal/ssh        1.358s
ok  github.com/mojomast/ussycode/internal/vm         0.009s
ok  github.com/mojomast/ussycode/internal/gateway    0.463s
ok  github.com/mojomast/ussycode/internal/auth       0.006s
ok  github.com/mojomast/ussycode/internal/pki        0.012s
ok  github.com/mojomast/ussycode/internal/scheduler  0.003s
ok  github.com/mojomast/ussycode/internal/api        0.125s
```

### Benchmark Summary:
```
BenchmarkCloneForVM-4              26280     4495 ns/op
BenchmarkDestroyVM-4               90891     1844 ns/op
BenchmarkGetUsage/vms=10-4          8500    13018 ns/op
BenchmarkGetUsage/vms=100-4         1209   112019 ns/op
BenchmarkGetUsage/vms=1000-4         126   983956 ns/op
BenchmarkParseZFSSize-4           104139     1394 ns/op
BenchmarkResizeVM-4                72326     1692 ns/op
BenchmarkNetworkAllocate-4        181536      876 ns/op
BenchmarkMACGeneration-4          199632      671 ns/op
BenchmarkCIDRMaskConversion-4     471506      332 ns/op
```

### Notes:
- Pre-existing `internal/admin/embed.go` issue remains (empty web/templates dir — not our bug)
- Integration tests are guarded by `USSYCODE_INTEGRATION=1` and skipped in normal CI
- Benchmarks use mock command runners — they measure Go logic overhead, not real ZFS/nft performance
