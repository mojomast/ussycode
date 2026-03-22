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
	jailer         *JailerConfig
	logger         *slog.Logger
}

// JailerConfig holds configuration for the Firecracker jailer.
// If Bin is empty, the jailer is disabled.
type JailerConfig struct {
	Bin           string // path to jailer binary
	UID           int    // unprivileged UID
	GID           int    // unprivileged GID
	ChrootBaseDir string // base dir for chroots
}

// Enabled returns true if the jailer is configured.
func (j *JailerConfig) Enabled() bool {
	return j != nil && j.Bin != ""
}

// FirecrackerVM represents a running Firecracker microVM.
type FirecrackerVM struct {
	machine    *firecracker.Machine
	cancelFunc context.CancelFunc
	socketPath string
	logPath    string
	cgroupPath string // per-VM cgroup directory (empty if cgroup not configured)
	jailedID   string // jailer VM ID (for chroot cleanup)
}

// NewFirecrackerBackend creates a new Firecracker VMM backend.
func NewFirecrackerBackend(firecrackerBin, kernelPath, dataDir string, logger *slog.Logger, jailer *JailerConfig) (*FirecrackerBackend, error) {
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

	// Validate jailer if configured
	if jailer.Enabled() {
		if _, err := exec.LookPath(jailer.Bin); err != nil {
			if _, err := os.Stat(jailer.Bin); err != nil {
				logger.Warn("jailer binary not found, disabling jailer", "path", jailer.Bin, "error", err)
				jailer = &JailerConfig{} // disable
			}
		}
		if jailer.Enabled() {
			if err := os.MkdirAll(jailer.ChrootBaseDir, 0755); err != nil {
				return nil, fmt.Errorf("create chroot base dir %s: %w", jailer.ChrootBaseDir, err)
			}
			logger.Info("jailer enabled", "bin", jailer.Bin, "uid", jailer.UID, "gid", jailer.GID, "chroot_base", jailer.ChrootBaseDir)
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
		jailer:         jailer,
		logger:         logger,
	}, nil
}

// StartVM boots a new Firecracker microVM with the given configuration.
func (fb *FirecrackerBackend) StartVM(ctx context.Context, opts *VMStartOptions) (*FirecrackerVM, error) {
	// Create per-VM runtime directory. When using the jailer, the socket path
	// must be relative to the jailed rootfs (for example /run/firecracker.sock),
	// otherwise Firecracker tries to bind a nested absolute path that does not
	// exist inside the chroot and exits before creating the API socket.
	vmRunDir := filepath.Join(fb.dataDir, "run", opts.VMID)
	if err := os.MkdirAll(vmRunDir, 0700); err != nil {
		return nil, fmt.Errorf("create vm run dir: %w", err)
	}

	socketPath := filepath.Join(vmRunDir, "firecracker.sock")
	if fb.jailer.Enabled() {
		socketPath = "/run/firecracker.sock"
	}
	logPath := filepath.Join(vmRunDir, "firecracker.log")

	// Remove stale jailer chroot state before starting.
	// If a prior stop/crash leaves files behind (for example /dev/net/tun),
	// the jailer can fail during startup with "File exists" while recreating
	// device nodes inside the chroot.
	if fb.jailer.Enabled() {
		staleChrootDir := filepath.Join(fb.jailer.ChrootBaseDir, "firecracker", opts.VMID)
		if staleChrootDir != "" && staleChrootDir != "/" {
			if err := os.RemoveAll(staleChrootDir); err != nil {
				return nil, fmt.Errorf("remove stale jailer chroot %s: %w", staleChrootDir, err)
			}
		}
	}

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
				RateLimiter: &models.RateLimiter{
					Bandwidth: &models.TokenBucket{
						Size:         firecracker.Int64(100 * 1024 * 1024), // 100 MB/s
						RefillTime:   firecracker.Int64(1000),              // refill every 1s
						OneTimeBurst: firecracker.Int64(100 * 1024 * 1024),
					},
					Ops: &models.TokenBucket{
						Size:         firecracker.Int64(5000), // 5000 IOPS
						RefillTime:   firecracker.Int64(1000),
						OneTimeBurst: firecracker.Int64(5000),
					},
				},
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
			RateLimiter: &models.RateLimiter{
				Bandwidth: &models.TokenBucket{
					Size:         firecracker.Int64(50 * 1024 * 1024), // 50 MB/s
					RefillTime:   firecracker.Int64(1000),
					OneTimeBurst: firecracker.Int64(50 * 1024 * 1024),
				},
				Ops: &models.TokenBucket{
					Size:         firecracker.Int64(3000), // 3000 IOPS
					RefillTime:   firecracker.Int64(1000),
					OneTimeBurst: firecracker.Int64(3000),
				},
			},
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
				InRateLimiter: &models.RateLimiter{
					Bandwidth: &models.TokenBucket{
						Size:         firecracker.Int64(12_500_000), // 100 Mbps
						RefillTime:   firecracker.Int64(1000),
						OneTimeBurst: firecracker.Int64(12_500_000),
					},
					Ops: &models.TokenBucket{
						Size:         firecracker.Int64(10000), // 10k pps
						RefillTime:   firecracker.Int64(1000),
						OneTimeBurst: firecracker.Int64(10000),
					},
				},
				OutRateLimiter: &models.RateLimiter{
					Bandwidth: &models.TokenBucket{
						Size:         firecracker.Int64(12_500_000), // 100 Mbps
						RefillTime:   firecracker.Int64(1000),
						OneTimeBurst: firecracker.Int64(12_500_000),
					},
					Ops: &models.TokenBucket{
						Size:         firecracker.Int64(10000), // 10k pps
						RefillTime:   firecracker.Int64(1000),
						OneTimeBurst: firecracker.Int64(10000),
					},
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

	var machine *firecracker.Machine
	if fb.jailer.Enabled() {
		// Use jailer for chroot isolation
		fcCfg.JailerCfg = &firecracker.JailerConfig{
			GID:            firecracker.Int(fb.jailer.GID),
			UID:            firecracker.Int(fb.jailer.UID),
			ID:             opts.VMID,
			NumaNode:       firecracker.Int(0),
			ExecFile:       fb.firecrackerBin,
			JailerBinary:   fb.jailer.Bin,
			ChrootBaseDir:  fb.jailer.ChrootBaseDir,
			Daemonize:      false,
			ChrootStrategy: firecracker.NewNaiveChrootStrategy(fb.kernelPath),
			Stdout:         logFile,
			Stderr:         logFile,
			CgroupVersion:  "2",
		}

		machine, err = firecracker.NewMachine(vmCtx, fcCfg)
	} else {
		// No jailer — use direct process runner
		cmd := firecracker.VMCommandBuilder{}.
			WithBin(fb.firecrackerBin).
			WithSocketPath(socketPath).
			WithStdout(logFile).
			WithStderr(logFile).
			Build(vmCtx)

		machine, err = firecracker.NewMachine(vmCtx, fcCfg,
			firecracker.WithProcessRunner(cmd),
		)
	}
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

	// Restrict socket permissions — only root should access the Firecracker API socket
	if err := os.Chmod(socketPath, 0600); err != nil {
		fb.logger.Warn("failed to restrict socket permissions", "path", socketPath, "error", err)
	}

	// Set up per-VM cgroup for resource isolation
	var cgroupPath string
	if pid, err := machine.PID(); err == nil {
		fb.logger.Info("firecracker VM started", "vm", opts.VMID, "pid", pid)
		cgroupPath, err = setupVMCgroup(opts.VMID, pid, opts.VCPU, opts.MemoryMB, fb.logger)
		if err != nil {
			fb.logger.Warn("failed to setup VM cgroup (VM will run without cgroup limits)",
				"vm", opts.VMID, "error", err)
		}
	}

	return &FirecrackerVM{
		machine:    machine,
		cancelFunc: cancel,
		socketPath: socketPath,
		logPath:    logPath,
		cgroupPath: cgroupPath,
		jailedID:   opts.VMID,
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

	// Cleanup cgroup
	if vm.cgroupPath != "" {
		if err := os.Remove(vm.cgroupPath); err != nil && !os.IsNotExist(err) {
			fb.logger.Warn("failed to remove VM cgroup", "path", vm.cgroupPath, "error", err)
		}
	}

	// Cleanup jailer chroot if it exists
	if fb.jailer.Enabled() {
		chrootDir := filepath.Join(fb.jailer.ChrootBaseDir, "firecracker", vm.jailedID)
		if chrootDir != "" && chrootDir != "/" {
			if err := os.RemoveAll(chrootDir); err != nil {
				fb.logger.Warn("failed to cleanup jailer chroot", "path", chrootDir, "error", err)
			}
		}
	}

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

// --- cgroup helpers ---

// parentCgroupPath is the cgroup path for the ussycode-dev service.
// VMs get per-VM subdirectories under this path.
const parentCgroupPath = "/sys/fs/cgroup/system.slice/ussycode-dev.service"

// setupVMCgroup creates a per-VM cgroup and moves the Firecracker process into it.
// This enforces CPU, memory, and PID limits on each VM independently.
//
// Cgroup v2 layout:
//
//	/sys/fs/cgroup/system.slice/ussycode-dev.service/vm-{id}/
//	  cpu.max       = "{quota} 100000"  (one vCPU = 100000)
//	  memory.max    = memoryMB * 1024 * 1024
//	  memory.swap.max = 0
//	  pids.max      = 4096
//	  cgroup.procs  = <firecracker PID>
func setupVMCgroup(vmID string, pid, vcpu, memoryMB int, logger *slog.Logger) (string, error) {
	cgroupDir := filepath.Join(parentCgroupPath, fmt.Sprintf("vm-%s", vmID))

	// Enable subtree control on parent first (idempotent)
	subtreeCtl := filepath.Join(parentCgroupPath, "cgroup.subtree_control")
	if err := os.WriteFile(subtreeCtl, []byte("+cpu +memory +pids"), 0644); err != nil {
		return "", fmt.Errorf("enable subtree_control: %w", err)
	}

	// Create per-VM cgroup directory
	if err := os.MkdirAll(cgroupDir, 0755); err != nil {
		return "", fmt.Errorf("create cgroup dir: %w", err)
	}

	// Set CPU limit: vcpu * 100000 quota per 100000 period
	cpuQuota := vcpu * 100000
	cpuMax := fmt.Sprintf("%d 100000", cpuQuota)
	if err := os.WriteFile(filepath.Join(cgroupDir, "cpu.max"), []byte(cpuMax), 0644); err != nil {
		logger.Warn("failed to set cpu.max", "vm", vmID, "error", err)
	}

	// Set memory limit
	memoryBytes := int64(memoryMB) * 1024 * 1024
	if err := os.WriteFile(filepath.Join(cgroupDir, "memory.max"), []byte(fmt.Sprintf("%d", memoryBytes)), 0644); err != nil {
		logger.Warn("failed to set memory.max", "vm", vmID, "error", err)
	}

	// Disable swap
	if err := os.WriteFile(filepath.Join(cgroupDir, "memory.swap.max"), []byte("0"), 0644); err != nil {
		logger.Warn("failed to set memory.swap.max", "vm", vmID, "error", err)
	}

	// Limit number of processes
	if err := os.WriteFile(filepath.Join(cgroupDir, "pids.max"), []byte("4096"), 0644); err != nil {
		logger.Warn("failed to set pids.max", "vm", vmID, "error", err)
	}

	// Move Firecracker process into the cgroup
	if err := os.WriteFile(filepath.Join(cgroupDir, "cgroup.procs"), []byte(fmt.Sprintf("%d", pid)), 0644); err != nil {
		return "", fmt.Errorf("move PID %d to cgroup: %w", pid, err)
	}

	logger.Info("VM cgroup configured",
		"vm", vmID,
		"cgroup", cgroupDir,
		"cpu_max", cpuMax,
		"memory_max_mb", memoryMB,
		"pids_max", 4096,
	)

	return cgroupDir, nil
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
