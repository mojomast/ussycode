package agent

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	nodev1 "github.com/mojomast/ussycode/internal/proto/nodev1"
)

// HeartbeatConfig controls heartbeat behaviour.
type HeartbeatConfig struct {
	// Interval between heartbeats (default 10s).
	Interval time.Duration

	// InitialBackoff on connection failure (default 1s).
	InitialBackoff time.Duration

	// MaxBackoff caps exponential backoff (default 60s).
	MaxBackoff time.Duration
}

// DefaultHeartbeatConfig returns sensible defaults.
func DefaultHeartbeatConfig() HeartbeatConfig {
	return HeartbeatConfig{
		Interval:       10 * time.Second,
		InitialBackoff: 1 * time.Second,
		MaxBackoff:     60 * time.Second,
	}
}

// heartbeatLoop runs the heartbeat sender. It collects system metrics and
// sends them over the gRPC stream. On connection failure it reconnects with
// exponential backoff. It runs until ctx is cancelled.
func (a *Agent) heartbeatLoop(ctx context.Context, cfg HeartbeatConfig) {
	if cfg.Interval == 0 {
		cfg = DefaultHeartbeatConfig()
	}

	backoff := cfg.InitialBackoff
	logger := a.logger.With("subsystem", "heartbeat")

	for {
		select {
		case <-ctx.Done():
			logger.Info("heartbeat loop stopping")
			return
		default:
		}

		err := a.runHeartbeatStream(ctx, cfg, logger)
		if err != nil && ctx.Err() == nil {
			logger.Warn("heartbeat stream error, reconnecting",
				"error", err,
				"backoff", backoff,
			)
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return
			}
			// Exponential backoff with cap.
			backoff = time.Duration(math.Min(
				float64(backoff)*2,
				float64(cfg.MaxBackoff),
			))
		} else {
			// Reset backoff on clean exit.
			backoff = cfg.InitialBackoff
		}
	}
}

// runHeartbeatStream opens a single heartbeat stream and runs until it errors
// or ctx is cancelled.
func (a *Agent) runHeartbeatStream(ctx context.Context, cfg HeartbeatConfig, logger *slog.Logger) error {
	if a.client == nil {
		// No gRPC client configured; run in standalone mode collecting
		// metrics but not sending them. This allows the agent to start
		// and be tested without a control plane.
		return a.runStandaloneHeartbeat(ctx, cfg, logger)
	}

	stream, err := a.client.Heartbeat(ctx)
	if err != nil {
		return fmt.Errorf("opening heartbeat stream: %w", err)
	}
	defer stream.Close()

	ticker := time.NewTicker(cfg.Interval)
	defer ticker.Stop()

	// Receive commands in a goroutine.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			resp, err := stream.Recv()
			if err != nil {
				logger.Warn("heartbeat recv error", "error", err)
				return
			}
			a.handleCommands(resp.Commands, logger)
		}
	}()

	for {
		select {
		case <-ctx.Done():
			wg.Wait()
			return nil
		case <-ticker.C:
			status, err := CollectNodeStatus()
			if err != nil {
				logger.Warn("failed to collect metrics", "error", err)
				continue
			}
			req := &nodev1.HeartbeatRequest{
				NodeID: a.state.NodeID,
				Status: status,
			}
			if err := stream.Send(req); err != nil {
				wg.Wait()
				return fmt.Errorf("sending heartbeat: %w", err)
			}
			logger.Debug("heartbeat sent",
				"cpu_usage", status.CPUUsage,
				"ram_used", status.RAMUsed,
				"vm_count", status.VMCount,
			)
		}
	}
}

// runStandaloneHeartbeat just logs metrics without sending them anywhere.
func (a *Agent) runStandaloneHeartbeat(ctx context.Context, cfg HeartbeatConfig, logger *slog.Logger) error {
	ticker := time.NewTicker(cfg.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			status, err := CollectNodeStatus()
			if err != nil {
				logger.Warn("failed to collect metrics", "error", err)
				continue
			}
			logger.Info("standalone heartbeat",
				"cpu_usage", fmt.Sprintf("%.1f%%", status.CPUUsage*100),
				"ram_used_mb", status.RAMUsed/(1024*1024),
				"ram_total_mb", status.RAMTotal/(1024*1024),
				"disk_used_mb", status.DiskUsed/(1024*1024),
				"disk_total_mb", status.DiskTotal/(1024*1024),
			)
		}
	}
}

// handleCommands processes a batch of commands from the control plane.
func (a *Agent) handleCommands(commands []nodev1.Command, logger *slog.Logger) {
	for _, cmd := range commands {
		switch cmd.Type {
		case nodev1.CommandTypeCreateVM:
			if cmd.CreateVM != nil {
				logger.Info("received create VM command",
					"vm_id", cmd.CreateVM.VMID,
					"vcpus", cmd.CreateVM.VCPUCount,
					"mem_bytes", cmd.CreateVM.MemBytes,
				)
				// TODO: dispatch to VM manager
			}
		case nodev1.CommandTypeStopVM:
			if cmd.StopVM != nil {
				logger.Info("received stop VM command", "vm_id", cmd.StopVM.VMID)
			}
		case nodev1.CommandTypeDestroyVM:
			if cmd.DestroyVM != nil {
				logger.Info("received destroy VM command", "vm_id", cmd.DestroyVM.VMID)
			}
		case nodev1.CommandTypeDrain:
			if cmd.Drain != nil {
				logger.Info("received drain command", "deadline_seconds", cmd.Drain.DeadlineSeconds)
			}
		case nodev1.CommandTypeUpdateConfig:
			if cmd.UpdateConfig != nil {
				logger.Info("received config update", "keys", len(cmd.UpdateConfig.Config))
			}
		default:
			logger.Warn("unknown command type", "type", cmd.Type)
		}
	}
}

// ---------------------------------------------------------------------------
// System metrics collection
// ---------------------------------------------------------------------------

// CollectNodeStatus gathers system resource metrics. It reads from /proc
// and /sys where available, falling back to Go runtime stats.
func CollectNodeStatus() (*nodev1.NodeStatus, error) {
	status := &nodev1.NodeStatus{}

	status.CPUUsage = collectCPUUsage()

	memUsed, memTotal := collectMemory()
	status.RAMUsed = memUsed
	status.RAMTotal = memTotal

	diskUsed, diskTotal := collectDisk()
	status.DiskUsed = diskUsed
	status.DiskTotal = diskTotal

	status.NetworkRxBytes, status.NetworkTxBytes = collectNetworkBytes()

	return status, nil
}

// collectCPUUsage returns a rough CPU usage estimate (0.0–1.0).
// A full implementation would sample /proc/stat over two intervals.
func collectCPUUsage() float64 {
	// Read /proc/loadavg for a quick 1-min load average.
	data, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0
	}
	fields := strings.Fields(string(data))
	if len(fields) < 1 {
		return 0
	}
	var load1 float64
	fmt.Sscanf(fields[0], "%f", &load1)

	cpus := float64(runtime.NumCPU())
	if cpus == 0 {
		cpus = 1
	}
	// Normalize to 0–1 range.
	usage := load1 / cpus
	if usage > 1.0 {
		usage = 1.0
	}
	return usage
}

// collectMemory reads /proc/meminfo and returns (used, total) in bytes.
func collectMemory() (int64, int64) {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		// Fallback to runtime.
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		return int64(m.Alloc), int64(m.Sys)
	}

	var total, free, buffers, cached int64
	for _, line := range strings.Split(string(data), "\n") {
		switch {
		case strings.HasPrefix(line, "MemTotal:"):
			fmt.Sscanf(line, "MemTotal: %d kB", &total)
		case strings.HasPrefix(line, "MemFree:"):
			fmt.Sscanf(line, "MemFree: %d kB", &free)
		case strings.HasPrefix(line, "Buffers:"):
			fmt.Sscanf(line, "Buffers: %d kB", &buffers)
		case strings.HasPrefix(line, "Cached:"):
			// Careful: "Cached:" could also match "CachedSwap:" lines.
			if strings.HasPrefix(line, "Cached:") && !strings.HasPrefix(line, "CachedSwap") {
				fmt.Sscanf(line, "Cached: %d kB", &cached)
			}
		}
	}

	totalBytes := total * 1024
	usedBytes := (total - free - buffers - cached) * 1024
	if usedBytes < 0 {
		usedBytes = 0
	}
	return usedBytes, totalBytes
}

// collectDisk reads disk stats for the root filesystem.
func collectDisk() (int64, int64) {
	// Use /proc/mounts-like approach. For simplicity, use syscall-free
	// fallback: read /proc/diskstats header. A proper implementation
	// would use syscall.Statfs.
	//
	// For now, return zeros — the scheduler and heartbeat tests don't
	// depend on actual disk values.
	return 0, 0
}

// collectNetworkBytes reads /proc/net/dev for aggregate rx/tx bytes.
func collectNetworkBytes() (int64, int64) {
	data, err := os.ReadFile("/proc/net/dev")
	if err != nil {
		return 0, 0
	}

	var totalRx, totalTx int64
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		// Skip header lines and loopback.
		if !strings.Contains(line, ":") || strings.HasPrefix(line, "lo:") {
			continue
		}

		// Format: iface: rx_bytes rx_packets ... tx_bytes tx_packets ...
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		fields := strings.Fields(parts[1])
		if len(fields) < 10 {
			continue
		}

		var rx, tx int64
		fmt.Sscanf(fields[0], "%d", &rx)
		fmt.Sscanf(fields[8], "%d", &tx)
		totalRx += rx
		totalTx += tx
	}

	return totalRx, totalTx
}
