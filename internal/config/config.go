package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

// Config holds all configuration for the exedevussy platform.
type Config struct {
	// Domain is the base domain for VM subdomains (e.g., "ussy.host")
	Domain string

	// SSHListenAddr is the address the SSH gateway listens on
	SSHListenAddr string

	// SSHHostKeyPath is the path to the SSH host key
	SSHHostKeyPath string

	// HTTPListenAddr is the address the HTTP API listens on
	HTTPListenAddr string

	// DataDir is the root directory for all persistent data
	DataDir string

	// DBPath is the path to the SQLite database
	DBPath string

	// CaddyAdminAddr is the Caddy admin API address
	CaddyAdminAddr string

	// VMM is the hypervisor backend: "firecracker" or "cloudhv"
	VMM string

	// KernelPath is the path to the guest kernel for microVMs
	KernelPath string

	// DefaultImage is the default container image for new VMs
	DefaultImage string

	// StorageBackend is "lvm" or "zfs"
	StorageBackend string

	// StoragePool is the LVM VG or ZFS pool name
	StoragePool string

	// NetworkBridge is the bridge interface for VM networking
	NetworkBridge string

	// NetworkSubnet is the CIDR for VM tap interfaces (e.g., "10.0.0.0/24")
	NetworkSubnet string

	// MaxVMsPerUser is the default VM limit per user
	MaxVMsPerUser int

	// DefaultCPU is the default vCPU count per VM
	DefaultCPU int

	// DefaultMemoryMB is the default memory in MB per VM
	DefaultMemoryMB int

	// DefaultDiskGB is the default disk size in GB per VM
	DefaultDiskGB int

	// TLSEmail is the email for Let's Encrypt certificate registration
	TLSEmail string

	// DNSProvider is the DNS provider for wildcard cert challenges (e.g., "cloudflare")
	DNSProvider string

	// DNSAPIToken is the API token for the DNS provider
	DNSAPIToken string

	// Debug enables debug logging
	Debug bool
}

// DefaultConfig returns a Config with sensible defaults.
func DefaultConfig() *Config {
	dataDir := envOrDefault("EXEDEVUSSY_DATA_DIR", "/var/lib/exedevussy")
	return &Config{
		Domain:          envOrDefault("EXEDEVUSSY_DOMAIN", "ussy.host"),
		SSHListenAddr:   envOrDefault("EXEDEVUSSY_SSH_ADDR", ":22"),
		SSHHostKeyPath:  envOrDefault("EXEDEVUSSY_SSH_HOST_KEY", filepath.Join(dataDir, "ssh_host_ed25519_key")),
		HTTPListenAddr:  envOrDefault("EXEDEVUSSY_HTTP_ADDR", ":8080"),
		DataDir:         dataDir,
		DBPath:          envOrDefault("EXEDEVUSSY_DB_PATH", filepath.Join(dataDir, "exedevussy.db")),
		CaddyAdminAddr:  envOrDefault("EXEDEVUSSY_CADDY_ADMIN", "http://localhost:2019"),
		VMM:             envOrDefault("EXEDEVUSSY_VMM", "firecracker"),
		KernelPath:      envOrDefault("EXEDEVUSSY_KERNEL", "/var/lib/exedevussy/vmlinux"),
		DefaultImage:    envOrDefault("EXEDEVUSSY_DEFAULT_IMAGE", "ussyuntu"),
		StorageBackend:  envOrDefault("EXEDEVUSSY_STORAGE", "lvm"),
		StoragePool:     envOrDefault("EXEDEVUSSY_STORAGE_POOL", "exedevussy"),
		NetworkBridge:   envOrDefault("EXEDEVUSSY_BRIDGE", "ussy0"),
		NetworkSubnet:   envOrDefault("EXEDEVUSSY_SUBNET", "10.0.0.0/24"),
		MaxVMsPerUser:   envOrDefaultInt("EXEDEVUSSY_MAX_VMS", 5),
		DefaultCPU:      envOrDefaultInt("EXEDEVUSSY_DEFAULT_CPU", 1),
		DefaultMemoryMB: envOrDefaultInt("EXEDEVUSSY_DEFAULT_MEM", 512),
		DefaultDiskGB:   envOrDefaultInt("EXEDEVUSSY_DEFAULT_DISK", 5),
		TLSEmail:        envOrDefault("EXEDEVUSSY_TLS_EMAIL", ""),
		DNSProvider:     envOrDefault("EXEDEVUSSY_DNS_PROVIDER", "cloudflare"),
		DNSAPIToken:     envOrDefault("EXEDEVUSSY_DNS_API_TOKEN", ""),
		Debug:           envOrDefault("EXEDEVUSSY_DEBUG", "") != "",
	}
}

// Validate checks that required configuration is present.
func (c *Config) Validate() error {
	if c.Domain == "" {
		return fmt.Errorf("domain is required")
	}
	if c.DataDir == "" {
		return fmt.Errorf("data directory is required")
	}
	return nil
}

func envOrDefault(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}

func envOrDefaultInt(key string, defaultVal int) int {
	if v := os.Getenv(key); v != "" {
		n, err := strconv.Atoi(v)
		if err == nil {
			return n
		}
	}
	return defaultVal
}
