// Package storage defines the StorageBackend interface and provides
// a ZFS-based implementation for managing VM disk storage.
//
// The StorageBackend interface is the contract between the VM manager
// and the underlying storage system. It supports cloning base images
// for new VMs, destroying VM storage, resizing disks, and querying
// per-user usage statistics.
package storage

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
)

// StorageBackend is the interface for VM disk storage operations.
// Implementations must be safe for concurrent use.
type StorageBackend interface {
	// CloneForVM creates a writable clone of a base image for a VM.
	// baseImage is the name of the cached base image (e.g., "ussyuntu").
	// vmID is a unique identifier for the VM (used in dataset/volume names).
	// Returns the device path (e.g., /dev/zvol/ussycode/vm-<vmID>) or an error.
	CloneForVM(ctx context.Context, baseImage, vmID string) (devicePath string, err error)

	// DestroyVM removes all storage for a VM (clone datasets, volumes).
	// It is safe to call if the VM's storage does not exist (idempotent).
	DestroyVM(ctx context.Context, vmID string) error

	// ResizeVM changes the disk quota/size for a VM.
	// newSize is a human-readable size string (e.g., "10G", "20G").
	ResizeVM(ctx context.Context, vmID, newSize string) error

	// GetUsage returns disk usage statistics for a user.
	// userID is used to filter datasets belonging to that user.
	GetUsage(ctx context.Context, userID string) (*UsageStats, error)
}

// UsageStats holds disk usage information for a user or pool.
type UsageStats struct {
	// TotalBytes is the total provisioned space across all VMs.
	TotalBytes int64

	// UsedBytes is the actual space consumed on disk.
	UsedBytes int64

	// VMCount is the number of VM datasets/volumes found.
	VMCount int
}

// CommandRunner abstracts exec.Command for testability.
// The default implementation runs real system commands;
// tests can inject a mock that records and replays commands.
type CommandRunner interface {
	// Run executes a command and returns its combined stdout/stderr output.
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// ZFSBackend implements StorageBackend using ZFS datasets and clones.
//
// Dataset layout:
//
//	<pool>/images/<baseImage>       — base image snapshot (read-only)
//	<pool>/images/<baseImage>@base  — snapshot used for cloning
//	<pool>/vms/<vmID>               — per-VM writable clone
//
// The ZFS backend uses lightweight clones (copy-on-write) so new VMs
// start almost instantly without copying the full base image.
type ZFSBackend struct {
	pool   string        // ZFS pool name (e.g., "ussycode")
	runner CommandRunner // command executor (mockable for tests)
	logger *slog.Logger
}

// NewZFSBackend creates a new ZFS storage backend.
// pool is the ZFS pool name (e.g., "ussycode").
// runner is the command executor (use DefaultRunner() for production).
func NewZFSBackend(pool string, runner CommandRunner, logger *slog.Logger) *ZFSBackend {
	return &ZFSBackend{
		pool:   pool,
		runner: runner,
		logger: logger,
	}
}

// CloneForVM creates a ZFS clone of the base image for a VM.
//
// Steps:
//  1. Verify the base image snapshot exists at <pool>/images/<baseImage>@base
//  2. Create a clone at <pool>/vms/<vmID> from that snapshot
//  3. Return the zvol device path
func (z *ZFSBackend) CloneForVM(ctx context.Context, baseImage, vmID string) (string, error) {
	snapshot := fmt.Sprintf("%s/images/%s@base", z.pool, baseImage)
	target := fmt.Sprintf("%s/vms/%s", z.pool, vmID)

	z.logger.Info("cloning base image for VM",
		"snapshot", snapshot,
		"target", target,
		"vm_id", vmID,
	)

	// Verify snapshot exists
	if _, err := z.runner.Run(ctx, "zfs", "list", "-t", "snapshot", "-H", snapshot); err != nil {
		return "", fmt.Errorf("base image snapshot %q not found: %w", snapshot, err)
	}

	// Create clone
	if _, err := z.runner.Run(ctx, "zfs", "clone", snapshot, target); err != nil {
		return "", fmt.Errorf("clone %q to %q: %w", snapshot, target, err)
	}

	devicePath := fmt.Sprintf("/dev/zvol/%s/vms/%s", z.pool, vmID)
	z.logger.Info("VM clone created", "device", devicePath, "vm_id", vmID)
	return devicePath, nil
}

// DestroyVM removes the ZFS dataset for a VM.
// Uses -r to recursively destroy any child datasets/snapshots.
// Idempotent: returns nil if the dataset doesn't exist.
func (z *ZFSBackend) DestroyVM(ctx context.Context, vmID string) error {
	target := fmt.Sprintf("%s/vms/%s", z.pool, vmID)

	z.logger.Info("destroying VM storage", "dataset", target, "vm_id", vmID)

	out, err := z.runner.Run(ctx, "zfs", "destroy", "-r", target)
	if err != nil {
		// Check if the dataset doesn't exist (idempotent)
		if strings.Contains(string(out), "does not exist") ||
			strings.Contains(string(out), "could not find") {
			z.logger.Debug("VM dataset already gone", "dataset", target, "vm_id", vmID)
			return nil
		}
		return fmt.Errorf("destroy %q: %s: %w", target, string(out), err)
	}

	z.logger.Info("VM storage destroyed", "dataset", target, "vm_id", vmID)
	return nil
}

// ResizeVM changes the volsize (or refquota) of a VM's ZFS dataset.
// newSize should be a ZFS-compatible size string (e.g., "10G", "20G").
func (z *ZFSBackend) ResizeVM(ctx context.Context, vmID, newSize string) error {
	target := fmt.Sprintf("%s/vms/%s", z.pool, vmID)

	z.logger.Info("resizing VM storage", "dataset", target, "new_size", newSize, "vm_id", vmID)

	// Try volsize first (for zvols), fall back to refquota (for datasets)
	_, err := z.runner.Run(ctx, "zfs", "set", fmt.Sprintf("volsize=%s", newSize), target)
	if err != nil {
		// Maybe it's a regular dataset, try refquota
		_, err2 := z.runner.Run(ctx, "zfs", "set", fmt.Sprintf("refquota=%s", newSize), target)
		if err2 != nil {
			return fmt.Errorf("resize %q to %s: volsize error: %w, refquota error: %v", target, newSize, err, err2)
		}
	}

	z.logger.Info("VM storage resized", "dataset", target, "new_size", newSize, "vm_id", vmID)
	return nil
}

// GetUsage returns disk usage for all VMs belonging to a user.
// It lists datasets under <pool>/vms/ that match the userID prefix pattern
// and aggregates their used and referenced space.
//
// Dataset naming convention: VMs are named as <vmID> where the vmID
// can be mapped to a userID by the caller. For simplicity, this
// implementation lists ALL VM datasets and lets the caller filter,
// or filters by a prefix pattern like "user-<userID>-".
func (z *ZFSBackend) GetUsage(ctx context.Context, userID string) (*UsageStats, error) {
	prefix := fmt.Sprintf("%s/vms/", z.pool)

	z.logger.Debug("getting storage usage", "prefix", prefix, "user_id", userID)

	// List all VM datasets with used and referenced sizes
	out, err := z.runner.Run(ctx, "zfs", "list", "-t", "filesystem,volume",
		"-H", "-o", "name,used,refer", "-r", fmt.Sprintf("%s/vms", z.pool))
	if err != nil {
		// If the vms dataset doesn't exist yet, return zero usage
		if strings.Contains(string(out), "does not exist") {
			return &UsageStats{}, nil
		}
		return nil, fmt.Errorf("list datasets: %s: %w", string(out), err)
	}

	stats := &UsageStats{}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	for _, line := range lines {
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}

		name := fields[0]
		// Skip the parent dataset itself
		if name == fmt.Sprintf("%s/vms", z.pool) {
			continue
		}

		// If userID filter is provided, only count matching VMs.
		// Convention: dataset name contains the userID as a prefix
		// e.g., "ussycode/vms/user-42-myvm"
		if userID != "" && !strings.Contains(name, userID) {
			continue
		}

		used := parseZFSSize(fields[1])
		refer := parseZFSSize(fields[2])

		stats.UsedBytes += used
		stats.TotalBytes += refer
		stats.VMCount++
	}

	return stats, nil
}

// parseZFSSize converts a ZFS human-readable size (e.g., "1.5G", "512M", "2048")
// to bytes. Returns 0 for unparseable values.
func parseZFSSize(s string) int64 {
	s = strings.TrimSpace(s)
	if s == "" || s == "-" || s == "0" {
		return 0
	}

	// Try parsing as raw bytes first
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return n
	}

	// Extract numeric part and suffix
	var multiplier int64 = 1
	numStr := s

	switch {
	case strings.HasSuffix(s, "T"):
		multiplier = 1024 * 1024 * 1024 * 1024
		numStr = strings.TrimSuffix(s, "T")
	case strings.HasSuffix(s, "G"):
		multiplier = 1024 * 1024 * 1024
		numStr = strings.TrimSuffix(s, "G")
	case strings.HasSuffix(s, "M"):
		multiplier = 1024 * 1024
		numStr = strings.TrimSuffix(s, "M")
	case strings.HasSuffix(s, "K"):
		multiplier = 1024
		numStr = strings.TrimSuffix(s, "K")
	}

	f, err := strconv.ParseFloat(numStr, 64)
	if err != nil {
		return 0
	}
	return int64(f * float64(multiplier))
}
