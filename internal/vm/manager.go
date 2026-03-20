package vm

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sync"

	"github.com/mojomast/ussycode/internal/db"
)

// Manager orchestrates VM lifecycle: image pulling, rootfs creation,
// network allocation, Firecracker boot, and cleanup.
type Manager struct {
	db      *db.DB
	images  *ImageManager
	network NetworkBackend
	fc      *FirecrackerBackend
	dataDir string
	logger  *slog.Logger

	// Track running VMs for shutdown/cleanup
	mu      sync.RWMutex
	running map[int64]*RunningVM // VM ID -> running state
}

// RunningVM holds runtime state for a running microVM.
type RunningVM struct {
	VMID          int64
	Name          string
	FirecrackerVM *FirecrackerVM
	NetworkConfig *NetworkConfig
	ImageConfig   *ImageConfig
}

// ManagerConfig holds the configuration for the VM manager.
type ManagerConfig struct {
	DataDir        string // base dir for all VM data
	FirecrackerBin string
	KernelPath     string
	BridgeName     string
	SubnetCIDR     string
}

// NewManager creates a new VM manager with all subsystems initialized.
func NewManager(database *db.DB, cfg *ManagerConfig, logger *slog.Logger) (*Manager, error) {
	// Ensure data directory structure
	for _, dir := range []string{
		cfg.DataDir,
		filepath.Join(cfg.DataDir, "images"),
		filepath.Join(cfg.DataDir, "disks"),
		filepath.Join(cfg.DataDir, "run"),
	} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return nil, fmt.Errorf("create dir %s: %w", dir, err)
		}
	}

	// Initialize image manager
	images, err := NewImageManager(filepath.Join(cfg.DataDir, "images"), logger.With("component", "images"))
	if err != nil {
		return nil, fmt.Errorf("init image manager: %w", err)
	}

	// Initialize network manager
	network, err := NewNetworkManager(cfg.BridgeName, cfg.SubnetCIDR, logger.With("component", "network"))
	if err != nil {
		return nil, fmt.Errorf("init network manager: %w", err)
	}

	// Initialize Firecracker backend
	fc, err := NewFirecrackerBackend(
		cfg.FirecrackerBin,
		cfg.KernelPath,
		cfg.DataDir,
		logger.With("component", "firecracker"),
	)
	if err != nil {
		return nil, fmt.Errorf("init firecracker: %w", err)
	}

	return &Manager{
		db:      database,
		images:  images,
		network: network,
		fc:      fc,
		dataDir: cfg.DataDir,
		logger:  logger,
		running: make(map[int64]*RunningVM),
	}, nil
}

// CreateAndStart creates a new VM: pulls the image, builds rootfs,
// allocates networking, and boots via Firecracker. Updates the DB record
// throughout the process.
func (m *Manager) CreateAndStart(ctx context.Context, vmID int64, name, imageRef string, vcpu, memoryMB int) error {
	m.logger.Info("creating and starting VM",
		"vm_id", vmID,
		"name", name,
		"image", imageRef,
		"vcpu", vcpu,
		"memory_mb", memoryMB,
	)

	// Update status to creating
	if err := m.db.UpdateVMStatus(ctx, vmID, "creating", nil, nil, nil, nil); err != nil {
		return fmt.Errorf("update status to creating: %w", err)
	}

	// 1. Ensure rootfs image exists (pull + extract + mkfs.ext4)
	rootfsPath, imgCfg, err := m.images.EnsureRootfs(ctx, imageRef)
	if err != nil {
		m.db.UpdateVMStatus(ctx, vmID, "error", nil, nil, nil, nil)
		return fmt.Errorf("ensure rootfs: %w", err)
	}

	// 2. Create a writable copy of the rootfs for this VM
	// (base image is shared/cached, each VM gets its own copy)
	vmRootfs := filepath.Join(m.dataDir, "disks", fmt.Sprintf("vm-%d-rootfs.ext4", vmID))
	if err := copyFile(rootfsPath, vmRootfs); err != nil {
		m.db.UpdateVMStatus(ctx, vmID, "error", nil, nil, nil, nil)
		return fmt.Errorf("copy rootfs: %w", err)
	}

	// 3. Create a persistent data disk for the user
	dataDiskPath := filepath.Join(m.dataDir, "disks", fmt.Sprintf("vm-%d-data.ext4", vmID))
	if _, err := os.Stat(dataDiskPath); os.IsNotExist(err) {
		if err := createEmptyExt4(ctx, dataDiskPath, 5*1024*1024*1024); err != nil { // 5GB
			m.db.UpdateVMStatus(ctx, vmID, "error", nil, nil, nil, nil)
			return fmt.Errorf("create data disk: %w", err)
		}
	}

	// 4. Allocate network (TAP device + IP)
	vmIDStr := fmt.Sprintf("%d", vmID)
	netCfg, err := m.network.AllocateNetwork(vmIDStr)
	if err != nil {
		m.db.UpdateVMStatus(ctx, vmID, "error", nil, nil, nil, nil)
		return fmt.Errorf("allocate network: %w", err)
	}

	// 5. Boot the VM
	fcVM, err := m.fc.StartVM(ctx, &VMStartOptions{
		VMID:          vmIDStr,
		RootfsPath:    vmRootfs,
		DataDiskPath:  dataDiskPath,
		VCPU:          vcpu,
		MemoryMB:      memoryMB,
		NetworkConfig: netCfg,
	})
	if err != nil {
		m.network.ReleaseNetwork(vmIDStr, netCfg.TapDevice)
		m.db.UpdateVMStatus(ctx, vmID, "error", nil, nil, nil, nil)
		return fmt.Errorf("start VM: %w", err)
	}

	// 6. Update DB with runtime info
	pid := fcVM.Pid()
	if err := m.db.UpdateVMStatus(ctx, vmID, "running",
		&netCfg.TapDevice,
		&netCfg.GuestIP,
		&netCfg.MacAddress,
		&pid,
	); err != nil {
		m.logger.Error("failed to update VM status after start", "error", err)
	}

	// 7. Track running VM
	m.mu.Lock()
	m.running[vmID] = &RunningVM{
		VMID:          vmID,
		Name:          name,
		FirecrackerVM: fcVM,
		NetworkConfig: netCfg,
		ImageConfig:   imgCfg,
	}
	m.mu.Unlock()

	m.logger.Info("VM started successfully",
		"vm_id", vmID,
		"name", name,
		"ip", netCfg.GuestIP,
		"pid", pid,
	)

	return nil
}

// Start boots an existing stopped VM that already has disk images.
// Unlike CreateAndStart, this doesn't pull images or create new disks.
func (m *Manager) Start(ctx context.Context, vmID int64, name, image string, vcpu, memoryMB int) error {
	m.logger.Info("starting existing VM",
		"vm_id", vmID,
		"name", name,
		"vcpu", vcpu,
		"memory_mb", memoryMB,
	)

	// Check that the rootfs disk exists
	vmRootfs := filepath.Join(m.dataDir, "disks", fmt.Sprintf("vm-%d-rootfs.ext4", vmID))
	if _, err := os.Stat(vmRootfs); os.IsNotExist(err) {
		return fmt.Errorf("rootfs disk not found for VM %d (may need to recreate)", vmID)
	}

	dataDiskPath := filepath.Join(m.dataDir, "disks", fmt.Sprintf("vm-%d-data.ext4", vmID))

	// Update status to creating
	if err := m.db.UpdateVMStatus(ctx, vmID, "creating", nil, nil, nil, nil); err != nil {
		return fmt.Errorf("update status to creating: %w", err)
	}

	// Allocate network
	vmIDStr := fmt.Sprintf("%d", vmID)
	netCfg, err := m.network.AllocateNetwork(vmIDStr)
	if err != nil {
		m.db.UpdateVMStatus(ctx, vmID, "error", nil, nil, nil, nil)
		return fmt.Errorf("allocate network: %w", err)
	}

	// Boot the VM
	fcVM, err := m.fc.StartVM(ctx, &VMStartOptions{
		VMID:          vmIDStr,
		RootfsPath:    vmRootfs,
		DataDiskPath:  dataDiskPath,
		VCPU:          vcpu,
		MemoryMB:      memoryMB,
		NetworkConfig: netCfg,
	})
	if err != nil {
		m.network.ReleaseNetwork(vmIDStr, netCfg.TapDevice)
		m.db.UpdateVMStatus(ctx, vmID, "error", nil, nil, nil, nil)
		return fmt.Errorf("start VM: %w", err)
	}

	// Update DB
	pid := fcVM.Pid()
	if err := m.db.UpdateVMStatus(ctx, vmID, "running",
		&netCfg.TapDevice,
		&netCfg.GuestIP,
		&netCfg.MacAddress,
		&pid,
	); err != nil {
		m.logger.Error("failed to update VM status after start", "error", err)
	}

	// Track
	m.mu.Lock()
	m.running[vmID] = &RunningVM{
		VMID:          vmID,
		Name:          name,
		FirecrackerVM: fcVM,
		NetworkConfig: netCfg,
	}
	m.mu.Unlock()

	m.logger.Info("VM started successfully",
		"vm_id", vmID,
		"name", name,
		"ip", netCfg.GuestIP,
		"pid", pid,
	)

	return nil
}

// Stop shuts down a running VM and cleans up resources.
func (m *Manager) Stop(ctx context.Context, vmID int64) error {
	m.mu.Lock()
	rv, ok := m.running[vmID]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("VM %d is not running", vmID)
	}
	delete(m.running, vmID)
	m.mu.Unlock()

	m.logger.Info("stopping VM", "vm_id", vmID, "name", rv.Name)

	// Stop Firecracker
	if err := m.fc.StopVM(ctx, rv.FirecrackerVM); err != nil {
		m.logger.Error("failed to stop firecracker", "vm_id", vmID, "error", err)
	}

	// Release network
	vmIDStr := fmt.Sprintf("%d", vmID)
	if rv.NetworkConfig != nil {
		m.network.ReleaseNetwork(vmIDStr, rv.NetworkConfig.TapDevice)
	}

	// Update DB
	if err := m.db.UpdateVMStatus(ctx, vmID, "stopped", nil, nil, nil, nil); err != nil {
		m.logger.Error("failed to update VM status", "vm_id", vmID, "error", err)
	}

	return nil
}

// Destroy stops a VM (if running) and removes all its disk files.
func (m *Manager) Destroy(ctx context.Context, vmID int64) error {
	// Stop if running
	m.mu.RLock()
	_, isRunning := m.running[vmID]
	m.mu.RUnlock()

	if isRunning {
		if err := m.Stop(ctx, vmID); err != nil {
			m.logger.Warn("error stopping VM before destroy", "vm_id", vmID, "error", err)
		}
	}

	// Remove disk files
	rootfs := filepath.Join(m.dataDir, "disks", fmt.Sprintf("vm-%d-rootfs.ext4", vmID))
	dataDisk := filepath.Join(m.dataDir, "disks", fmt.Sprintf("vm-%d-data.ext4", vmID))
	runDir := filepath.Join(m.dataDir, "run", fmt.Sprintf("%d", vmID))

	for _, path := range []string{rootfs, dataDisk} {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			m.logger.Warn("failed to remove disk", "path", path, "error", err)
		}
	}

	os.RemoveAll(runDir)

	// Remove DB record
	if err := m.db.DeleteVM(ctx, vmID); err != nil {
		return fmt.Errorf("delete VM record: %w", err)
	}

	m.logger.Info("VM destroyed", "vm_id", vmID)
	return nil
}

// GetRunning returns the runtime info for a running VM, or nil if not running.
func (m *Manager) GetRunning(vmID int64) *RunningVM {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.running[vmID]
}

// ListRunning returns all currently running VMs.
func (m *Manager) ListRunning() []*RunningVM {
	m.mu.RLock()
	defer m.mu.RUnlock()

	vms := make([]*RunningVM, 0, len(m.running))
	for _, rv := range m.running {
		vms = append(vms, rv)
	}
	return vms
}

// ShutdownAll stops all running VMs. Called during graceful server shutdown.
func (m *Manager) ShutdownAll(ctx context.Context) {
	m.mu.RLock()
	ids := make([]int64, 0, len(m.running))
	for id := range m.running {
		ids = append(ids, id)
	}
	m.mu.RUnlock()

	for _, id := range ids {
		if err := m.Stop(ctx, id); err != nil {
			m.logger.Error("failed to stop VM during shutdown", "vm_id", id, "error", err)
		}
	}
}

// CloneDisks copies the rootfs and data disks from one VM to another.
// The new VM must already have a DB record. The clone is stopped (not booted).
func (m *Manager) CloneDisks(ctx context.Context, srcVMID, dstVMID int64) error {
	srcRootfs := filepath.Join(m.dataDir, "disks", fmt.Sprintf("vm-%d-rootfs.ext4", srcVMID))
	srcData := filepath.Join(m.dataDir, "disks", fmt.Sprintf("vm-%d-data.ext4", srcVMID))
	dstRootfs := filepath.Join(m.dataDir, "disks", fmt.Sprintf("vm-%d-rootfs.ext4", dstVMID))
	dstData := filepath.Join(m.dataDir, "disks", fmt.Sprintf("vm-%d-data.ext4", dstVMID))

	m.logger.Info("cloning VM disks", "src", srcVMID, "dst", dstVMID)

	// Copy rootfs
	if _, err := os.Stat(srcRootfs); err == nil {
		if err := copyFile(srcRootfs, dstRootfs); err != nil {
			return fmt.Errorf("copy rootfs: %w", err)
		}
	}

	// Copy data disk
	if _, err := os.Stat(srcData); err == nil {
		if err := copyFile(srcData, dstData); err != nil {
			// Cleanup rootfs copy on failure
			os.Remove(dstRootfs)
			return fmt.Errorf("copy data disk: %w", err)
		}
	}

	return nil
}

// ImageManager returns the image manager (for cache operations, etc.).
func (m *Manager) ImageManager() *ImageManager {
	return m.images
}

// NetworkManager returns the network manager.
func (m *Manager) NetworkManager() NetworkBackend {
	return m.network
}

// --- helpers ---

// copyFile copies a file from src to dst using a simple read/write.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := out.ReadFrom(in); err != nil {
		return err
	}

	return out.Sync()
}

// createEmptyExt4 creates an empty ext4 filesystem of the given size.
func createEmptyExt4(ctx context.Context, path string, sizeBytes int64) error {
	// Create sparse file
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := f.Truncate(sizeBytes); err != nil {
		f.Close()
		return err
	}
	f.Close()

	// Format as ext4
	cmd := exec.CommandContext(ctx, "mkfs.ext4", "-F", "-q", path)
	if out, err := cmd.CombinedOutput(); err != nil {
		os.Remove(path)
		return fmt.Errorf("mkfs.ext4: %s: %w", string(out), err)
	}

	return nil
}
