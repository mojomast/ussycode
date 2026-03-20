package storage

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"testing"
)

// BenchmarkCloneForVM benchmarks the CloneForVM path with a mock runner.
// This measures the overhead of the Go logic (string formatting, logging,
// error handling) without real ZFS calls.
func BenchmarkCloneForVM(b *testing.B) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	runner := newMockRunner()
	zfs := NewZFSBackend("bench", runner, logger)

	ctx := context.Background()

	// Pre-configure mock responses for all VMs we'll create
	for i := 0; i < b.N; i++ {
		vmID := fmt.Sprintf("bench-vm-%08d", i)
		snapshot := fmt.Sprintf("bench/images/ussyuntu@base")
		target := fmt.Sprintf("bench/vms/%s", vmID)
		runner.SetResponse("zfs list -t snapshot -H "+snapshot, nil, nil)
		runner.SetResponse("zfs clone "+snapshot+" "+target, nil, nil)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		vmID := fmt.Sprintf("bench-vm-%08d", i)
		_, err := zfs.CloneForVM(ctx, "ussyuntu", vmID)
		if err != nil {
			b.Fatalf("CloneForVM() error = %v", err)
		}
	}
}

// BenchmarkDestroyVM benchmarks the DestroyVM path with a mock runner.
func BenchmarkDestroyVM(b *testing.B) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	runner := newMockRunner()
	zfs := NewZFSBackend("bench", runner, logger)

	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		vmID := fmt.Sprintf("bench-vm-%08d", i)
		// Default mock response is success (nil, nil)
		err := zfs.DestroyVM(ctx, vmID)
		if err != nil {
			b.Fatalf("DestroyVM() error = %v", err)
		}
	}
}

// BenchmarkGetUsage benchmarks usage parsing with varying numbers of VM datasets.
func BenchmarkGetUsage(b *testing.B) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	for _, numVMs := range []int{10, 100, 1000} {
		b.Run(fmt.Sprintf("vms=%d", numVMs), func(b *testing.B) {
			runner := newMockRunner()
			zfs := NewZFSBackend("bench", runner, logger)

			// Build mock ZFS list output with numVMs datasets
			var lines []string
			lines = append(lines, "bench/vms\t128K\t128K") // parent dataset
			for j := 0; j < numVMs; j++ {
				lines = append(lines, fmt.Sprintf("bench/vms/user-1-vm-%04d\t1.5G\t5G", j))
			}
			output := strings.Join(lines, "\n")
			runner.SetResponse("zfs list -t filesystem,volume -H -o name,used,refer -r bench/vms",
				[]byte(output), nil)

			ctx := context.Background()

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				stats, err := zfs.GetUsage(ctx, "user-1")
				if err != nil {
					b.Fatalf("GetUsage() error = %v", err)
				}
				if stats.VMCount != numVMs {
					b.Fatalf("VMCount = %d, want %d", stats.VMCount, numVMs)
				}
			}
		})
	}
}

// BenchmarkParseZFSSize benchmarks the ZFS size parser with various inputs.
func BenchmarkParseZFSSize(b *testing.B) {
	inputs := []string{"0", "1024", "1K", "512M", "1.5G", "2.5T", "-", ""}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, s := range inputs {
			_ = parseZFSSize(s)
		}
	}
}

// BenchmarkResizeVM benchmarks the resize path (volsize succeeds on first try).
func BenchmarkResizeVM(b *testing.B) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	runner := newMockRunner()
	zfs := NewZFSBackend("bench", runner, logger)

	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		vmID := fmt.Sprintf("bench-vm-%08d", i)
		// Default mock response is success — volsize set works on first try
		err := zfs.ResizeVM(ctx, vmID, "20G")
		if err != nil {
			b.Fatalf("ResizeVM() error = %v", err)
		}
	}
}
