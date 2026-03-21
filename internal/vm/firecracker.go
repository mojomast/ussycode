package vm

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	firecracker "github.com/firecracker-microvm/firecracker-go-sdk"
	"github.com/firecracker-microvm/firecracker-go-sdk/client/models"
)

// FirecrackerBackend implements VMM operations using Firecracker.
type FirecrackerBackend struct {
	firecrackerBin string // path to firecracker binary
	kernelPath     string // path to vmlinux guest kernel
	initrdPath     string // path to initrd image
	dataDir        string // base dir for VM runtime files
	logger         *slog.Logger
}

// FirecrackerVM represents a running Firecracker microVM.
type FirecrackerVM struct {
	machine    *firecracker.Machine
	cancelFunc context.CancelFunc
	socketPath string
	logPath    string
}

// NewFirecrackerBackend creates a new Firecracker VMM backend.
func NewFirecrackerBackend(firecrackerBin, kernelPath, dataDir string, logger *slog.Logger) (*FirecrackerBackend, error) {
	// Verify firecracker binary exists
	if _, err := exec.LookPath(firecrackerBin); err != nil {
		// Check common locations
		alternatives := []string{
			"/usr/local/bin/firecracker",
			"/usr/bin/firecracker",
			filepath.Join(dataDir, "bin", "firecracker"),
		}
		found := false
		for _, alt := range alternatives {
			if _, err := os.Stat(alt); err == nil {
				firecrackerBin = alt
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("firecracker binary not found: %w", err)
		}
	}

	// Verify kernel exists
	if _, err := os.Stat(kernelPath); err != nil {
		return nil, fmt.Errorf("kernel not found at %s: %w", kernelPath, err)
	}

	initrdPath := filepath.Join(dataDir, "initrd.img")
	if _, err := os.Stat(initrdPath); err != nil {
		fallbacks := []string{
			"/boot/initrd.img-6.12.74+deb13+1-cloud-amd64",
			"/boot/initrd.img-6.12.74+deb13+1-amd64",
		}
		for _, candidate := range fallbacks {
			if _, err := os.Stat(candidate); err == nil {
				initrdPath = candidate
				break
			}
		}
		if _, err := os.Stat(initrdPath); err != nil {
			initrdPath = ""
		}
	}

	// Ensure runtime directory exists
	runDir := filepath.Join(dataDir, "run")
	if err := os.MkdirAll(runDir, 0755); err != nil {
		return nil, fmt.Errorf("create run dir: %w", err)
	}

	return &FirecrackerBackend{
		firecrackerBin: firecrackerBin,
		kernelPath:     kernelPath,
		initrdPath:     initrdPath,
		dataDir:        dataDir,
		logger:         logger,
	}, nil
}

// StartVM boots a new Firecracker microVM with the given configuration.
func (fb *FirecrackerBackend) StartVM(ctx context.Context, opts *VMStartOptions) (*FirecrackerVM, error) {
	// Create per-VM runtime directory
	vmRunDir := filepath.Join(fb.dataDir, "run", opts.VMID)
	if err := os.MkdirAll(vmRunDir, 0755); err != nil {
		return nil, fmt.Errorf("create vm run dir: %w", err)
	}

	socketPath := filepath.Join(vmRunDir, "firecracker.sock")
	logPath := filepath.Join(vmRunDir, "firecracker.log")

	// Remove stale socket if it exists
	os.Remove(socketPath)

	// Build kernel boot args
	bootArgs := "console=ttyS0 reboot=k panic=1 rootwait pci=off"
	if opts.NetworkConfig != nil {
		// Configure static IP via kernel boot args
		bootArgs += fmt.Sprintf(" ip=%s::%s:%s::eth0:off",
			opts.NetworkConfig.GuestIP,
			opts.NetworkConfig.GatewayIP,
			cidrMaskToNetmask(opts.NetworkConfig.SubnetMask),
		)
	}

	// Create firecracker config
	fcCfg := firecracker.Config{
		SocketPath:      socketPath,
		KernelImagePath: fb.kernelPath,
		InitrdPath:      fb.initrdPath,
		KernelArgs:      bootArgs,
		MachineCfg: models.MachineConfiguration{
			VcpuCount:  firecracker.Int64(int64(opts.VCPU)),
			MemSizeMib: firecracker.Int64(int64(opts.MemoryMB)),
		},
		Drives: []models.Drive{
			{
				DriveID:      firecracker.String("rootfs"),
				PathOnHost:   firecracker.String(opts.RootfsPath),
				IsRootDevice: firecracker.Bool(true),
				IsReadOnly:   firecracker.Bool(false),
			},
		},
	}

	// Add persistent data disk if provided
	if opts.DataDiskPath != "" {
		fcCfg.Drives = append(fcCfg.Drives, models.Drive{
			DriveID:      firecracker.String("data"),
			PathOnHost:   firecracker.String(opts.DataDiskPath),
			IsRootDevice: firecracker.Bool(false),
			IsReadOnly:   firecracker.Bool(false),
		})
	}

	// Add network interface if configured
	if opts.NetworkConfig != nil {
		fcCfg.NetworkInterfaces = []firecracker.NetworkInterface{
			{
				StaticConfiguration: &firecracker.StaticNetworkConfiguration{
					MacAddress:  opts.NetworkConfig.MacAddress,
					HostDevName: opts.NetworkConfig.TapDevice,
				},
			},
		}
	}

	// Create machine
	vmCtx, cancel := context.WithCancel(ctx)

	logFile, err := os.Create(logPath)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("create log file: %w", err)
	}

	cmd := firecracker.VMCommandBuilder{}.
		WithBin(fb.firecrackerBin).
		WithSocketPath(socketPath).
		WithStdout(logFile).
		WithStderr(logFile).
		Build(vmCtx)

	machine, err := firecracker.NewMachine(vmCtx, fcCfg,
		firecracker.WithProcessRunner(cmd),
	)
	if err != nil {
		cancel()
		logFile.Close()
		return nil, fmt.Errorf("create firecracker machine: %w", err)
	}

	fb.logger.Info("starting firecracker VM",
		"vm", opts.VMID,
		"vcpu", opts.VCPU,
		"memory_mb", opts.MemoryMB,
		"rootfs", opts.RootfsPath,
		"initrd", fb.initrdPath,
	)

	if err := machine.Start(vmCtx); err != nil {
		cancel()
		logFile.Close()
		return nil, fmt.Errorf("start firecracker: %w", err)
	}

	if pid, err := machine.PID(); err == nil {
		fb.logger.Info("firecracker VM started", "vm", opts.VMID, "pid", pid)
	}

	return &FirecrackerVM{
		machine:    machine,
		cancelFunc: cancel,
		socketPath: socketPath,
		logPath:    logPath,
	}, nil
}

// StopVM gracefully shuts down a Firecracker VM.
func (fb *FirecrackerBackend) StopVM(ctx context.Context, vm *FirecrackerVM) error {
	if vm == nil || vm.machine == nil {
		return nil
	}

	pid, _ := vm.machine.PID()
	fb.logger.Info("stopping firecracker VM", "pid", pid)

	// Try graceful shutdown first (SendCtrlAltDel)
	shutdownCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	if err := vm.machine.Shutdown(shutdownCtx); err != nil {
		fb.logger.Warn("graceful shutdown failed, forcing stop", "error", err)
		// Force stop
		vm.machine.StopVMM()
	}

	// Wait for the process to exit
	vm.machine.Wait(ctx)

	// Cancel the VM context
	vm.cancelFunc()

	// Cleanup socket
	os.Remove(vm.socketPath)

	return nil
}

// Pid returns the PID of the Firecracker process.
func (vm *FirecrackerVM) Pid() int64 {
	if vm == nil || vm.machine == nil {
		return 0
	}
	pid, err := vm.machine.PID()
	if err != nil {
		return 0
	}
	return int64(pid)
}

// IsRunning checks if the Firecracker process is still alive.
func (vm *FirecrackerVM) IsRunning() bool {
	if vm == nil || vm.machine == nil {
		return false
	}
	pid, err := vm.machine.PID()
	if err != nil || pid <= 0 {
		return false
	}
	// Send signal 0 to check if process exists
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}

// VMStartOptions holds configuration for starting a new VM.
type VMStartOptions struct {
	VMID          string
	RootfsPath    string
	DataDiskPath  string // optional persistent disk
	VCPU          int
	MemoryMB      int
	NetworkConfig *NetworkConfig
	Env           map[string]string // environment variables for init
}

// cidrMaskToNetmask converts a CIDR prefix length (e.g., "24") to a
// dotted-decimal netmask (e.g., "255.255.255.0").
func cidrMaskToNetmask(cidr string) string {
	bits, err := strconv.Atoi(cidr)
	if err != nil || bits < 0 || bits > 32 {
		return "255.255.255.0" // safe default
	}
	mask := net.CIDRMask(bits, 32)
	return fmt.Sprintf("%d.%d.%d.%d", mask[0], mask[1], mask[2], mask[3])
}
