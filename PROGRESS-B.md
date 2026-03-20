# Track B: Ussyverse Server Pool — Progress

## Status: All B.1–B.7 tasks complete

### Module Path Note
The module has already been renamed to `github.com/mojomast/ussycode` by Track A.
All new code uses the new module path. **No rebase needed.**

---

## B.1: gRPC Protocol Definition ✅
- **proto/node/v1/node.proto** — Full protobuf definition (documentation)
- **internal/proto/nodev1/node.go** — Hand-written Go structs with JSON tags matching the proto
- **internal/proto/nodev1/service.go** — gRPC service interfaces (NodeServiceServer, NodeServiceClient, HeartbeatStream)

Notes:
- When `protoc` tooling is set up, the hand-written code should be replaced with generated code
- The interfaces are designed to be compatible with real gRPC generated stubs

## B.2: Agent Binary ✅
- **cmd/ussyverse-agent/main.go** — CLI with subcommands: `join`, `run`, `status`, `version`, `help`
- **internal/agent/agent.go** — Agent struct with Start(), Stop(), Join() methods
  - Ed25519 keypair generation and storage
  - State persistence (state.json in data dir)
  - Certificate storage
  - Graceful shutdown via context

## B.3: mTLS Certificate Management ✅
- **internal/pki/ca.go** — Full PKI implementation:
  - Ed25519 root CA generation
  - Intermediate CA signed by root
  - Short-lived node certificate issuance (configurable, default 24h)
  - Signed join token generation (time-limited, single-use)
  - Certificate verification against CA chain
  - PEM import/export for persistence
- **internal/pki/ca_test.go** — 7 tests covering:
  - CA generation, cert issuance, verification
  - Cross-CA rejection (cert from different CA fails verification)
  - Join token lifecycle (generate, validate, single-use, expired, tampered)
  - CA round-trip persistence (export PEM → reload → cross-verify)

## B.4: Heartbeat & Health ✅
- **internal/agent/heartbeat.go** — Agent-side heartbeat:
  - System metrics collection (CPU from /proc/loadavg, RAM from /proc/meminfo, network from /proc/net/dev)
  - Heartbeat loop with exponential backoff on connection loss
  - Standalone mode (logs metrics without gRPC) for development
  - Command handler dispatcher
- **internal/controlplane/nodemanager.go** — Control plane node manager:
  - Node registration and tracking
  - Heartbeat processing with command dispatch
  - Timeout-based health transitions: Healthy → Unknown (30s) → Offline (5min) → Dead (1hr)
  - Periodic timeout checker goroutine
  - Dead-node callback for triggering VM rescheduling

## B.5: WireGuard Mesh ✅
- **internal/mesh/wireguard.go** — WireGuard management:
  - WireGuardManager interface (CreateInterface, DeleteInterface, AddPeer, RemovePeer, ListPeers)
  - StubWireGuardManager for development/testing (in-memory, no real WireGuard)
  - WireGuardConfig and WireGuardPeer types
- **internal/mesh/allocator.go** — IP subnet allocation:
  - Allocates /24 subnets from 100.64.0.0/10 (Carrier-Grade NAT range)
  - 16,384 possible node subnets
  - Thread-safe with mutex
  - Import/export for DB persistence
  - Custom allocator constructor for testing

**TODO:** Production WireGuard implementation needs tailscale imports for magicsock/DERP integration. The interface is ready — only the implementation needs to be swapped.

## B.6: VM Placement Scheduler ✅
- **internal/scheduler/scheduler.go** — Two-phase scheduler:
  - Phase 1 (Filter): Ready, Healthy, Not Draining, Sufficient RAM/CPU/Disk, Trust level
  - Phase 2 (Score): BinPacking(0.4) + Spread(0.2) + Locality(0.2) + Freshness(0.1) + Trust(0.1)
  - Scheduler interface with PlaceVM, DrainNode, ListNodes
  - NodeProvider interface for abstracting node data source
  - Configurable score weights
- **internal/scheduler/scheduler_test.go** — 10 tests:
  - Basic placement, no suitable nodes, trust filtering
  - Draining nodes skipped, unhealthy nodes skipped
  - Locality preference, bin-packing preference
  - Drain integration, list nodes, stress test (100 nodes)

## B.7: Agent Installer Script ✅
- **deploy/install-agent.sh** — Production installer:
  - OS/arch detection (linux/amd64, linux/arm64)
  - KVM availability check
  - Agent binary download (placeholder URL)
  - systemd service creation with security hardening
  - Data directory setup
  - Cluster join
  - `--dry-run` and `--skip-kvm-check` options
  - Colored output, error handling

---

## Files Created (17 files)

| File | Purpose |
|------|---------|
| `proto/node/v1/node.proto` | Protobuf definition (documentation) |
| `internal/proto/nodev1/node.go` | Go message types |
| `internal/proto/nodev1/service.go` | gRPC service interfaces |
| `cmd/ussyverse-agent/main.go` | Agent CLI binary |
| `internal/agent/agent.go` | Agent core logic |
| `internal/agent/heartbeat.go` | Heartbeat + metrics collection |
| `internal/pki/ca.go` | Certificate authority |
| `internal/pki/ca_test.go` | PKI tests (7 tests) |
| `internal/controlplane/nodemanager.go` | Node tracking + health |
| `internal/mesh/wireguard.go` | WireGuard interface + stub |
| `internal/mesh/allocator.go` | IP subnet allocator |
| `internal/scheduler/scheduler.go` | VM placement scheduler |
| `internal/scheduler/scheduler_test.go` | Scheduler tests (10 tests) |
| `deploy/install-agent.sh` | Agent installer script |
| `PROGRESS-B.md` | This file |

## Build Status
```
go build ./internal/proto/nodev1/...   ✅
go build ./internal/agent/...          ✅
go build ./internal/pki/...            ✅
go build ./internal/controlplane/...   ✅
go build ./internal/mesh/...           ✅
go build ./internal/scheduler/...      ✅
go build ./cmd/ussyverse-agent/...     ✅
go test  ./internal/pki/...            ✅ (7 tests pass)
go test  ./internal/scheduler/...      ✅ (10 tests pass)
```

Note: Pre-existing packages (db, vm, ssh, proxy) have compilation errors from the module rename or missing dependencies. These are not related to Track B changes.
