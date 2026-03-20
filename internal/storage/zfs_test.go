package storage

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"testing"
)

// mockCommand records a single command invocation for verification.
type mockCommand struct {
	Name string
	Args []string
}

// mockRunner is a CommandRunner that records commands and returns
// pre-configured responses. Used for testing without real ZFS.
type mockRunner struct {
	// commands records all commands that were Run.
	commands []mockCommand

	// responses maps "name arg1 arg2 ..." to (output, error).
	// Use SetResponse to configure.
	responses map[string]mockResponse
}

type mockResponse struct {
	output []byte
	err    error
}

func newMockRunner() *mockRunner {
	return &mockRunner{
		responses: make(map[string]mockResponse),
	}
}

// SetResponse configures the mock to return the given output and error
// when the specified command is run. The key is "name arg1 arg2 ...".
func (m *mockRunner) SetResponse(key string, output []byte, err error) {
	m.responses[key] = mockResponse{output: output, err: err}
}

func (m *mockRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	cmd := mockCommand{Name: name, Args: args}
	m.commands = append(m.commands, cmd)

	key := name + " " + strings.Join(args, " ")
	if resp, ok := m.responses[key]; ok {
		return resp.output, resp.err
	}

	// Default: command succeeds with empty output
	return nil, nil
}

// CommandCount returns the number of commands recorded.
func (m *mockRunner) CommandCount() int {
	return len(m.commands)
}

// LastCommand returns the last recorded command.
func (m *mockRunner) LastCommand() mockCommand {
	if len(m.commands) == 0 {
		return mockCommand{}
	}
	return m.commands[len(m.commands)-1]
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func TestCloneForVM_Success(t *testing.T) {
	runner := newMockRunner()
	zfs := NewZFSBackend("testpool", runner, testLogger())

	// Configure mock: snapshot exists, clone succeeds
	runner.SetResponse("zfs list -t snapshot -H testpool/images/ussyuntu@base", nil, nil)
	runner.SetResponse("zfs clone testpool/images/ussyuntu@base testpool/vms/vm-42", nil, nil)

	devicePath, err := zfs.CloneForVM(context.Background(), "ussyuntu", "vm-42")
	if err != nil {
		t.Fatalf("CloneForVM() error = %v", err)
	}

	expected := "/dev/zvol/testpool/vms/vm-42"
	if devicePath != expected {
		t.Errorf("devicePath = %q, want %q", devicePath, expected)
	}

	if runner.CommandCount() != 2 {
		t.Errorf("expected 2 commands, got %d", runner.CommandCount())
	}
}

func TestCloneForVM_SnapshotNotFound(t *testing.T) {
	runner := newMockRunner()
	zfs := NewZFSBackend("testpool", runner, testLogger())

	// Snapshot doesn't exist
	runner.SetResponse("zfs list -t snapshot -H testpool/images/ussyuntu@base",
		[]byte("cannot open 'testpool/images/ussyuntu@base': dataset does not exist"),
		fmt.Errorf("exit status 1"))

	_, err := zfs.CloneForVM(context.Background(), "ussyuntu", "vm-42")
	if err == nil {
		t.Fatal("CloneForVM() expected error for missing snapshot, got nil")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error = %q, want to contain 'not found'", err.Error())
	}

	// Should only have run the list command, not the clone
	if runner.CommandCount() != 1 {
		t.Errorf("expected 1 command (list only), got %d", runner.CommandCount())
	}
}

func TestCloneForVM_CloneFails(t *testing.T) {
	runner := newMockRunner()
	zfs := NewZFSBackend("testpool", runner, testLogger())

	// Snapshot exists but clone fails
	runner.SetResponse("zfs list -t snapshot -H testpool/images/ussyuntu@base", nil, nil)
	runner.SetResponse("zfs clone testpool/images/ussyuntu@base testpool/vms/vm-42",
		[]byte("cannot create 'testpool/vms/vm-42': dataset already exists"),
		fmt.Errorf("exit status 1"))

	_, err := zfs.CloneForVM(context.Background(), "ussyuntu", "vm-42")
	if err == nil {
		t.Fatal("CloneForVM() expected error for failed clone, got nil")
	}
}

func TestDestroyVM_Success(t *testing.T) {
	runner := newMockRunner()
	zfs := NewZFSBackend("testpool", runner, testLogger())

	runner.SetResponse("zfs destroy -r testpool/vms/vm-42", nil, nil)

	err := zfs.DestroyVM(context.Background(), "vm-42")
	if err != nil {
		t.Fatalf("DestroyVM() error = %v", err)
	}

	if runner.CommandCount() != 1 {
		t.Errorf("expected 1 command, got %d", runner.CommandCount())
	}

	cmd := runner.LastCommand()
	if cmd.Name != "zfs" {
		t.Errorf("command name = %q, want %q", cmd.Name, "zfs")
	}
	expectedArgs := []string{"destroy", "-r", "testpool/vms/vm-42"}
	for i, arg := range expectedArgs {
		if i >= len(cmd.Args) || cmd.Args[i] != arg {
			t.Errorf("args[%d] = %q, want %q", i, cmd.Args[i], arg)
		}
	}
}

func TestDestroyVM_Idempotent(t *testing.T) {
	runner := newMockRunner()
	zfs := NewZFSBackend("testpool", runner, testLogger())

	// Dataset doesn't exist — should return nil (idempotent)
	runner.SetResponse("zfs destroy -r testpool/vms/vm-99",
		[]byte("cannot open 'testpool/vms/vm-99': dataset does not exist"),
		fmt.Errorf("exit status 1"))

	err := zfs.DestroyVM(context.Background(), "vm-99")
	if err != nil {
		t.Fatalf("DestroyVM() should be idempotent, got error = %v", err)
	}
}

func TestDestroyVM_RealError(t *testing.T) {
	runner := newMockRunner()
	zfs := NewZFSBackend("testpool", runner, testLogger())

	// Actual ZFS error (not "does not exist")
	runner.SetResponse("zfs destroy -r testpool/vms/vm-42",
		[]byte("cannot destroy 'testpool/vms/vm-42': dataset is busy"),
		fmt.Errorf("exit status 1"))

	err := zfs.DestroyVM(context.Background(), "vm-42")
	if err == nil {
		t.Fatal("DestroyVM() expected error for busy dataset, got nil")
	}
	if !strings.Contains(err.Error(), "busy") {
		t.Errorf("error = %q, want to contain 'busy'", err.Error())
	}
}

func TestResizeVM_Volsize(t *testing.T) {
	runner := newMockRunner()
	zfs := NewZFSBackend("testpool", runner, testLogger())

	// volsize set succeeds
	runner.SetResponse("zfs set volsize=20G testpool/vms/vm-42", nil, nil)

	err := zfs.ResizeVM(context.Background(), "vm-42", "20G")
	if err != nil {
		t.Fatalf("ResizeVM() error = %v", err)
	}

	if runner.CommandCount() != 1 {
		t.Errorf("expected 1 command, got %d", runner.CommandCount())
	}
}

func TestResizeVM_FallbackToRefquota(t *testing.T) {
	runner := newMockRunner()
	zfs := NewZFSBackend("testpool", runner, testLogger())

	// volsize fails (it's a regular dataset, not a zvol), refquota succeeds
	runner.SetResponse("zfs set volsize=20G testpool/vms/vm-42",
		[]byte("cannot set property for 'testpool/vms/vm-42': 'volsize' is not a valid property for datasets"),
		fmt.Errorf("exit status 1"))
	runner.SetResponse("zfs set refquota=20G testpool/vms/vm-42", nil, nil)

	err := zfs.ResizeVM(context.Background(), "vm-42", "20G")
	if err != nil {
		t.Fatalf("ResizeVM() with refquota fallback error = %v", err)
	}

	if runner.CommandCount() != 2 {
		t.Errorf("expected 2 commands (volsize attempt + refquota), got %d", runner.CommandCount())
	}
}

func TestResizeVM_BothFail(t *testing.T) {
	runner := newMockRunner()
	zfs := NewZFSBackend("testpool", runner, testLogger())

	runner.SetResponse("zfs set volsize=20G testpool/vms/vm-42",
		nil, fmt.Errorf("volsize error"))
	runner.SetResponse("zfs set refquota=20G testpool/vms/vm-42",
		nil, fmt.Errorf("refquota error"))

	err := zfs.ResizeVM(context.Background(), "vm-42", "20G")
	if err == nil {
		t.Fatal("ResizeVM() expected error when both methods fail, got nil")
	}
}

func TestGetUsage_Success(t *testing.T) {
	runner := newMockRunner()
	zfs := NewZFSBackend("testpool", runner, testLogger())

	// Mock ZFS list output with tab-separated fields
	output := strings.Join([]string{
		"testpool/vms\t128K\t128K",
		"testpool/vms/user-42-vm1\t1.5G\t5G",
		"testpool/vms/user-42-vm2\t512M\t10G",
		"testpool/vms/user-99-vm1\t2G\t5G",
	}, "\n")

	runner.SetResponse("zfs list -t filesystem,volume -H -o name,used,refer -r testpool/vms",
		[]byte(output), nil)

	stats, err := zfs.GetUsage(context.Background(), "user-42")
	if err != nil {
		t.Fatalf("GetUsage() error = %v", err)
	}

	if stats.VMCount != 2 {
		t.Errorf("VMCount = %d, want 2", stats.VMCount)
	}

	// 1.5G + 512M used
	expectedUsed := int64(1.5*1024*1024*1024) + int64(512*1024*1024)
	if stats.UsedBytes != expectedUsed {
		t.Errorf("UsedBytes = %d, want %d", stats.UsedBytes, expectedUsed)
	}

	// 5G + 10G total (referenced)
	expectedTotal := int64(5*1024*1024*1024) + int64(10*1024*1024*1024)
	if stats.TotalBytes != expectedTotal {
		t.Errorf("TotalBytes = %d, want %d", stats.TotalBytes, expectedTotal)
	}
}

func TestGetUsage_EmptyPool(t *testing.T) {
	runner := newMockRunner()
	zfs := NewZFSBackend("testpool", runner, testLogger())

	// Pool vms dataset doesn't exist yet
	runner.SetResponse("zfs list -t filesystem,volume -H -o name,used,refer -r testpool/vms",
		[]byte("cannot open 'testpool/vms': dataset does not exist"),
		fmt.Errorf("exit status 1"))

	stats, err := zfs.GetUsage(context.Background(), "user-42")
	if err != nil {
		t.Fatalf("GetUsage() for empty pool error = %v", err)
	}

	if stats.VMCount != 0 {
		t.Errorf("VMCount = %d, want 0", stats.VMCount)
	}
	if stats.UsedBytes != 0 {
		t.Errorf("UsedBytes = %d, want 0", stats.UsedBytes)
	}
}

func TestGetUsage_AllVMs(t *testing.T) {
	runner := newMockRunner()
	zfs := NewZFSBackend("testpool", runner, testLogger())

	output := strings.Join([]string{
		"testpool/vms\t128K\t128K",
		"testpool/vms/vm-1\t1G\t5G",
		"testpool/vms/vm-2\t2G\t10G",
	}, "\n")

	runner.SetResponse("zfs list -t filesystem,volume -H -o name,used,refer -r testpool/vms",
		[]byte(output), nil)

	// Empty userID = get all VMs
	stats, err := zfs.GetUsage(context.Background(), "")
	if err != nil {
		t.Fatalf("GetUsage() error = %v", err)
	}

	if stats.VMCount != 2 {
		t.Errorf("VMCount = %d, want 2", stats.VMCount)
	}
}

func TestParseZFSSize(t *testing.T) {
	tests := []struct {
		input string
		want  int64
	}{
		{"0", 0},
		{"-", 0},
		{"", 0},
		{"1024", 1024},
		{"1K", 1024},
		{"1M", 1024 * 1024},
		{"1G", 1024 * 1024 * 1024},
		{"1T", 1024 * 1024 * 1024 * 1024},
		{"1.5G", int64(1.5 * 1024 * 1024 * 1024)},
		{"512M", 512 * 1024 * 1024},
		{"2.5T", int64(2.5 * 1024 * 1024 * 1024 * 1024)},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := parseZFSSize(tt.input)
			if got != tt.want {
				t.Errorf("parseZFSSize(%q) = %d, want %d", tt.input, got, tt.want)
			}
		})
	}
}

// TestStorageBackendInterface verifies ZFSBackend satisfies the interface.
func TestStorageBackendInterface(t *testing.T) {
	var _ StorageBackend = (*ZFSBackend)(nil)
}
