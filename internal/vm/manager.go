package vm

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/mojomast/ussycode/internal/db"
	gossh "golang.org/x/crypto/ssh"
)

const ussycodeOpencodeConfig = `{
  "$schema": "https://opencode.ai/config.json",
  "instructions": ["instructions/ussycode-runtime.md"],
  "provider": {
    "zai": {
      "npm": "@ai-sdk/openai-compatible",
      "name": "Zai (GLM via ussycode)",
      "options": {
        "apiKey": "{env:OPENCODE_API_KEY}",
        "baseURL": "{env:OPENCODE_BASE_URL}"
      },
      "models": {
        "glm-5": { "name": "GLM-5" },
        "glm-5-turbo": { "name": "GLM-5 Turbo" },
        "glm-4.7": { "name": "GLM-4.7" },
        "glm-4.7-flash": { "name": "GLM-4.7 Flash" },
        "glm-4.6": { "name": "GLM-4.6" },
        "glm-4.6v": { "name": "GLM-4.6V" },
        "glm-4.5": { "name": "GLM-4.5" },
        "glm-4.5-air": { "name": "GLM-4.5 Air" },
        "glm-4.5-flash": { "name": "GLM-4.5 Flash" },
        "glm-4.5v": { "name": "GLM-4.5V" }
      }
    }
  },
  "model": "zai/glm-5"
}
`

const ussycodeOpencodeInstruction = "You are running inside a ussycode VM.\n\nWhen a user asks you to run, preview, host, expose, or share a web app from this VM, load the ussycode-web-proxy skill and follow it.\n\nDefault behavior in this environment:\n- bind web servers to 0.0.0.0, not localhost\n- prefer port 8080 because ussycode proxies that port automatically\n- the public URL is https://$USSYCODE_VM_NAME.$USSYCODE_PUBLIC_DOMAIN (read both env vars to construct it)\n"

const ussycodeOpencodeSkill = "---\nname: ussycode-web-proxy\ndescription: Expose web apps correctly from a ussycode VM by binding to 0.0.0.0:8080 and reporting the public proxy URL.\n---\n\n## When to use me\n\nUse this when the user wants to run, preview, host, expose, share, or remotely access an HTTP app from inside a ussycode VM.\n\n## What to do\n\n1. Read the env vars `USSYCODE_VM_NAME` and `USSYCODE_PUBLIC_DOMAIN` to construct the public URL.\n2. The public URL is always `https://$USSYCODE_VM_NAME.$USSYCODE_PUBLIC_DOMAIN`.\n3. Bind services to `0.0.0.0`, not `127.0.0.1`.\n4. Prefer port `8080` because ussycode proxies that port automatically.\n5. If a framework defaults to another port, change it to `8080` unless the user explicitly wants something else.\n6. If the app is already running on the wrong host or port, fix the command/config rather than just reporting a localhost URL.\n\n## Public URL construction\n\n**Always** read the env vars to build the URL. Example: if `USSYCODE_VM_NAME=mild-owl` and `USSYCODE_PUBLIC_DOMAIN=dev.ussyco.de`, the public URL is `https://mild-owl.dev.ussyco.de`.\n\nDo NOT guess the domain. Do NOT use `vmname.ussyco.de` — always include the full domain from `USSYCODE_PUBLIC_DOMAIN`.\n\n## Common commands\n\n- `python3 -m http.server 8080 --bind 0.0.0.0`\n- `uvicorn app:app --host 0.0.0.0 --port 8080`\n- `streamlit run app.py --server.address 0.0.0.0 --server.port 8080`\n- `npm run dev -- --host 0.0.0.0 --port 8080`\n- `vite --host 0.0.0.0 --port 8080`\n- `next dev -H 0.0.0.0 -p 8080`\n\n## Verify\n\n- confirm the app listens on `0.0.0.0:8080`\n- tell the user the public URL (from env vars), not a localhost URL\n"

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
	JailerBin      string // path to jailer binary (empty = disabled)
	JailerUID      int    // unprivileged UID for jailed VMs
	JailerGID      int    // unprivileged GID for jailed VMs
	ChrootBaseDir  string // base dir for jailer chroots
}

// NewManager creates a new VM manager with all subsystems initialized.
func NewManager(database *db.DB, cfg *ManagerConfig, logger *slog.Logger) (*Manager, error) {
	// Ensure data directory structure
	for _, dir := range []string{
		cfg.DataDir,
		filepath.Join(cfg.DataDir, "images"),
		filepath.Join(cfg.DataDir, "disks"),
		filepath.Join(cfg.DataDir, "run"),
		filepath.Join(cfg.DataDir, "keys"),
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
		&JailerConfig{
			Bin:           cfg.JailerBin,
			UID:           cfg.JailerUID,
			GID:           cfg.JailerGID,
			ChrootBaseDir: cfg.ChrootBaseDir,
		},
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

func (m *Manager) SeedAuthorizedKeys(ctx context.Context, vmID int64, keys []string) error {
	rootfs := filepath.Join(m.dataDir, "disks", fmt.Sprintf("vm-%d-rootfs.ext4", vmID))
	if _, err := os.Stat(rootfs); err != nil {
		return fmt.Errorf("stat rootfs: %w", err)
	}

	if err := m.configureGuestSSH(ctx, rootfs); err != nil {
		return err
	}

	if len(keys) == 0 {
		return nil
	}

	tmpFile, err := os.CreateTemp("", "ussycode-authorized-keys-*.txt")
	if err != nil {
		return fmt.Errorf("create temp keys file: %w", err)
	}
	tmpPath := tmpFile.Name()
	content := strings.Join(keys, "\n") + "\n"
	if _, err := tmpFile.WriteString(content); err != nil {
		tmpFile.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("write temp keys file: %w", err)
	}
	tmpFile.Close()
	defer os.Remove(tmpPath)

	commands := []string{
		"rm /home/ussycode/.ssh/authorized_keys",
		fmt.Sprintf("write %s /home/ussycode/.ssh/authorized_keys", tmpPath),
		"set_inode_field /home/ussycode/.ssh/authorized_keys uid 1001",
		"set_inode_field /home/ussycode/.ssh/authorized_keys gid 1001",
		"set_inode_field /home/ussycode/.ssh/authorized_keys mode 0100600",
		"set_inode_field /home/ussycode/.ssh uid 1001",
		"set_inode_field /home/ussycode/.ssh gid 1001",
		"set_inode_field /home/ussycode/.ssh mode 040700",
		"set_inode_field /home/ussycode uid 1001",
		"set_inode_field /home/ussycode gid 1001",
	}

	for _, cmdText := range commands {
		cmd := exec.CommandContext(ctx, "debugfs", "-w", "-R", cmdText, rootfs)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("debugfs %q: %s: %w", cmdText, string(out), err)
		}
	}

	return nil
}

func (m *Manager) configureGuestSSH(ctx context.Context, rootfs string) error {
	if err := rewriteExt4File(ctx, rootfs, "/etc/ssh/sshd_config", 0, 0, "0100644", func(s string) string {
		if strings.Contains(s, "\nUsePAM yes\n") {
			s = strings.ReplaceAll(s, "\nUsePAM yes\n", "\nUsePAM no\n")
		}
		if !strings.Contains(s, "\nUsePAM no\n") {
			if !strings.HasSuffix(s, "\n") {
				s += "\n"
			}
			s += "UsePAM no\n"
		}
		return s
	}); err != nil {
		return fmt.Errorf("patch sshd_config: %w", err)
	}

	if err := rewriteExt4File(ctx, rootfs, "/etc/shadow", 0, 42, "0100640", func(s string) string {
		lines := strings.Split(s, "\n")
		for i, line := range lines {
			if !strings.HasPrefix(line, "ussycode:") {
				continue
			}
			parts := strings.Split(line, ":")
			if len(parts) > 1 && (strings.HasPrefix(parts[1], "!") || strings.HasPrefix(parts[1], "*")) {
				parts[1] = "x"
				lines[i] = strings.Join(parts, ":")
			}
			break
		}
		return strings.Join(lines, "\n")
	}); err != nil {
		return fmt.Errorf("patch shadow: %w", err)
	}

	if err := rewriteExt4File(ctx, rootfs, "/usr/bin/sudo", 0, 0, "0104755", func(s string) string {
		return s
	}); err != nil {
		return fmt.Errorf("patch sudo permissions: %w", err)
	}

	if err := rewriteExt4File(ctx, rootfs, "/etc/resolv.conf", 0, 0, "0100644", func(string) string {
		return "nameserver 1.1.1.1\nnameserver 8.8.8.8\n"
	}); err != nil {
		return fmt.Errorf("patch resolv.conf: %w", err)
	}

	if err := m.installOpencodeRuntimeFiles(ctx, rootfs); err != nil {
		return fmt.Errorf("install opencode runtime files: %w", err)
	}

	// Patch the init script so existing VMs pick up fixes without a
	// full image rebuild. The embedded content stays in sync with
	// images/ussyuntu/init-ussycode.sh via the copy in internal/vm/.
	if err := rewriteExt4File(ctx, rootfs, "/usr/local/bin/init-ussycode.sh", 0, 0, "0100755", func(string) string {
		return ussycodeInitScript
	}); err != nil {
		return fmt.Errorf("patch init-ussycode.sh: %w", err)
	}

	return nil
}

func (m *Manager) installOpencodeRuntimeFiles(ctx context.Context, rootfs string) error {
	base := filepath.Join("home", "ussycode", ".config", "opencode")
	if err := mkdirExt4(ctx, rootfs, "/home/ussycode/.config"); err != nil {
		return err
	}
	if err := mkdirExt4(ctx, rootfs, "/home/ussycode/.config/opencode"); err != nil {
		return err
	}
	if err := mkdirExt4(ctx, rootfs, "/home/ussycode/.config/opencode/instructions"); err != nil {
		return err
	}
	if err := mkdirExt4(ctx, rootfs, "/home/ussycode/.config/opencode/skills"); err != nil {
		return err
	}
	if err := mkdirExt4(ctx, rootfs, "/home/ussycode/.config/opencode/skills/ussycode-web-proxy"); err != nil {
		return err
	}

	if err := writeExt4File(ctx, rootfs, "/"+filepath.Join(base, "opencode.json"), ussycodeOpencodeConfig, 1001, 1001, "0100644"); err != nil {
		return err
	}
	if err := writeExt4File(ctx, rootfs, "/"+filepath.Join(base, "instructions", "ussycode-runtime.md"), ussycodeOpencodeInstruction, 1001, 1001, "0100644"); err != nil {
		return err
	}
	if err := writeExt4File(ctx, rootfs, "/"+filepath.Join(base, "skills", "ussycode-web-proxy", "SKILL.md"), ussycodeOpencodeSkill, 1001, 1001, "0100644"); err != nil {
		return err
	}
	return nil
}

func mkdirExt4(ctx context.Context, rootfs, dir string) error {
	cmd := exec.CommandContext(ctx, "debugfs", "-w", "-R", fmt.Sprintf("mkdir %s", dir), rootfs)
	out, err := cmd.CombinedOutput()
	if err != nil && !strings.Contains(string(out), "Ext2 directory already exists") {
		return fmt.Errorf("debugfs mkdir %s: %s: %w", dir, string(out), err)
	}
	return nil
}

func writeExt4File(ctx context.Context, rootfs, guestPath, content string, uid, gid int, mode string) error {
	tmpFile, err := os.CreateTemp("", "ussycode-ext4-write-*")
	if err != nil {
		return err
	}
	tmpPath := tmpFile.Name()
	if _, err := tmpFile.WriteString(content); err != nil {
		tmpFile.Close()
		os.Remove(tmpPath)
		return err
	}
	tmpFile.Close()
	defer os.Remove(tmpPath)

	commands := []string{
		fmt.Sprintf("rm %s", guestPath),
		fmt.Sprintf("write %s %s", tmpPath, guestPath),
		fmt.Sprintf("set_inode_field %s uid %d", guestPath, uid),
		fmt.Sprintf("set_inode_field %s gid %d", guestPath, gid),
		fmt.Sprintf("set_inode_field %s mode %s", guestPath, mode),
	}
	for _, cmdText := range commands {
		cmd := exec.CommandContext(ctx, "debugfs", "-w", "-R", cmdText, rootfs)
		out, err := cmd.CombinedOutput()
		if err != nil && !(strings.HasPrefix(cmdText, "rm ") && strings.Contains(string(out), "File not found")) {
			return fmt.Errorf("debugfs %q: %s: %w", cmdText, string(out), err)
		}
	}
	return nil
}

func rewriteExt4File(ctx context.Context, rootfs, guestPath string, uid, gid int, mode string, mutate func(string) string) error {
	tmpFile, err := os.CreateTemp("", "ussycode-ext4-file-*")
	if err != nil {
		return err
	}
	tmpPath := tmpFile.Name()
	tmpFile.Close()
	defer os.Remove(tmpPath)

	dumpCmd := exec.CommandContext(ctx, "debugfs", "-R", fmt.Sprintf("dump %s %s", guestPath, tmpPath), rootfs)
	if out, err := dumpCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("debugfs dump %s: %s: %w", guestPath, string(out), err)
	}

	data, err := os.ReadFile(tmpPath)
	if err != nil {
		return err
	}
	if err := os.WriteFile(tmpPath, []byte(mutate(string(data))), 0600); err != nil {
		return err
	}

	commands := []string{
		fmt.Sprintf("rm %s", guestPath),
		fmt.Sprintf("write %s %s", tmpPath, guestPath),
		fmt.Sprintf("set_inode_field %s uid %d", guestPath, uid),
		fmt.Sprintf("set_inode_field %s gid %d", guestPath, gid),
		fmt.Sprintf("set_inode_field %s mode %s", guestPath, mode),
	}
	for _, cmdText := range commands {
		cmd := exec.CommandContext(ctx, "debugfs", "-w", "-R", cmdText, rootfs)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("debugfs %q: %s: %w", cmdText, string(out), err)
		}
	}

	return nil
}

// CreateAndStart creates a new VM: pulls the image, builds rootfs,
// allocates networking, and boots via Firecracker. Updates the DB record
// throughout the process.
func (m *Manager) CreateAndStart(ctx context.Context, vmID int64, name, imageRef string, vcpu, memoryMB int, sshKeys []string) error {
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
	if err := m.SeedAuthorizedKeys(ctx, vmID, sshKeys); err != nil {
		m.db.UpdateVMStatus(ctx, vmID, "error", nil, nil, nil, nil)
		return fmt.Errorf("seed authorized keys: %w", err)
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
	if err := m.network.SetupBridge(); err != nil {
		m.db.UpdateVMStatus(ctx, vmID, "error", nil, nil, nil, nil)
		return fmt.Errorf("setup bridge: %w", err)
	}
	netCfg, err := m.network.AllocateNetwork(vmIDStr)
	if err != nil {
		m.db.UpdateVMStatus(ctx, vmID, "error", nil, nil, nil, nil)
		return fmt.Errorf("allocate network: %w", err)
	}

	// 5. Boot the VM
	if fw, ok := m.network.(*NetworkManager); ok {
		if err := fw.firewall.AddVMRules(ctx, vmIDStr, netCfg.TapDevice, netCfg.GuestIP, fw.bridge); err != nil {
			m.network.ReleaseNetwork(vmIDStr, netCfg.TapDevice)
			m.db.UpdateVMStatus(ctx, vmID, "error", nil, nil, nil, nil)
			return fmt.Errorf("add VM firewall rules: %w", err)
		}
	}

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
func (m *Manager) Start(ctx context.Context, vmID int64, name, image string, vcpu, memoryMB int, sshKeys []string) error {
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
	if err := m.SeedAuthorizedKeys(ctx, vmID, sshKeys); err != nil {
		m.db.UpdateVMStatus(ctx, vmID, "error", nil, nil, nil, nil)
		return fmt.Errorf("seed authorized keys: %w", err)
	}

	dataDiskPath := filepath.Join(m.dataDir, "disks", fmt.Sprintf("vm-%d-data.ext4", vmID))

	// Update status to creating
	if err := m.db.UpdateVMStatus(ctx, vmID, "creating", nil, nil, nil, nil); err != nil {
		return fmt.Errorf("update status to creating: %w", err)
	}

	// Allocate network
	vmIDStr := fmt.Sprintf("%d", vmID)
	if err := m.network.SetupBridge(); err != nil {
		m.db.UpdateVMStatus(ctx, vmID, "error", nil, nil, nil, nil)
		return fmt.Errorf("setup bridge: %w", err)
	}
	netCfg, err := m.network.AllocateNetwork(vmIDStr)
	if err != nil {
		m.db.UpdateVMStatus(ctx, vmID, "error", nil, nil, nil, nil)
		return fmt.Errorf("allocate network: %w", err)
	}

	// Boot the VM
	if fw, ok := m.network.(*NetworkManager); ok {
		if err := fw.firewall.AddVMRules(ctx, vmIDStr, netCfg.TapDevice, netCfg.GuestIP, fw.bridge); err != nil {
			m.network.ReleaseNetwork(vmIDStr, netCfg.TapDevice)
			m.db.UpdateVMStatus(ctx, vmID, "error", nil, nil, nil, nil)
			return fmt.Errorf("add VM firewall rules: %w", err)
		}
	}

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
		if fw, ok := m.network.(*NetworkManager); ok {
			_ = fw.firewall.RemoveVMRules(ctx, vmIDStr, rv.NetworkConfig.TapDevice, fw.bridge)
		}
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

// EnsureUserKey generates an ed25519 keypair for the given user if one
// doesn't already exist, and returns the path to the private key file.
// Keys are stored at $DATA_DIR/keys/user-<id> (private) and
// $DATA_DIR/keys/user-<id>.pub (public).
func (m *Manager) EnsureUserKey(userID int64) (string, error) {
	keyPath := m.UserKeyPath(userID)
	if _, err := os.Stat(keyPath); err == nil {
		return keyPath, nil // already exists
	}

	m.logger.Info("generating per-user gateway key", "user_id", userID)

	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", fmt.Errorf("generate ed25519 key for user %d: %w", userID, err)
	}

	pemBlock, err := gossh.MarshalPrivateKey(privateKey, "")
	if err != nil {
		return "", fmt.Errorf("marshal private key for user %d: %w", userID, err)
	}

	pemData := pem.EncodeToMemory(pemBlock)
	if err := os.WriteFile(keyPath, pemData, 0600); err != nil {
		return "", fmt.Errorf("write private key for user %d: %w", userID, err)
	}

	// Also write the public key for convenience
	signer, err := gossh.NewSignerFromKey(privateKey)
	if err != nil {
		return "", fmt.Errorf("create signer for user %d: %w", userID, err)
	}
	pubKeyStr := string(gossh.MarshalAuthorizedKey(signer.PublicKey()))
	pubPath := keyPath + ".pub"
	if err := os.WriteFile(pubPath, []byte(pubKeyStr), 0644); err != nil {
		return "", fmt.Errorf("write public key for user %d: %w", userID, err)
	}

	m.logger.Info("per-user gateway key generated", "user_id", userID, "path", keyPath)
	return keyPath, nil
}

// UserKeyPath returns the path to a user's gateway private key file.
func (m *Manager) UserKeyPath(userID int64) string {
	return filepath.Join(m.dataDir, "keys", fmt.Sprintf("user-%d", userID))
}

// UserPublicKey reads and returns the user's gateway public key in
// authorized_keys format. Generates the keypair first if it doesn't exist.
func (m *Manager) UserPublicKey(userID int64) (string, error) {
	pubPath := m.UserKeyPath(userID) + ".pub"

	// Ensure the key exists
	if _, err := m.EnsureUserKey(userID); err != nil {
		return "", err
	}

	data, err := os.ReadFile(pubPath)
	if err != nil {
		return "", fmt.Errorf("read public key for user %d: %w", userID, err)
	}

	return strings.TrimSpace(string(data)), nil
}
