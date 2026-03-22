package config

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

// Config holds all configuration for the ussycode platform.
// Values are resolved in priority order: CLI flags > environment variables > defaults.
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

	// MetadataAddr is the address the metadata service listens on.
	// In production this is typically exposed to guests as 169.254.169.254:80,
	// while the host-side service itself defaults to :8083.
	MetadataAddr string

	// AuthProxyAddr is the address the auth proxy listens on (for Caddy forward_auth)
	AuthProxyAddr string

	// AuthProxyURL is the URL Caddy uses to reach the auth proxy via forward_auth.
	// Defaults to http://localhost:9876. Only needs to differ from the default when
	// the auth proxy is on a different host or port than the default.
	AuthProxyURL string

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

	// FirecrackerBin is the path to the firecracker binary
	FirecrackerBin string

	// AdminListenAddr is the address the admin web panel listens on
	AdminListenAddr string

	// LLMEncryptSecret is the secret for encrypting stored LLM API keys
	LLMEncryptSecret string

	// SMTPListenAddr is the address the inbound SMTP server listens on
	SMTPListenAddr string

	// SMTPRelay is the address of the outbound SMTP relay (host:port)
	SMTPRelay string

	// SMTPFromAddress is the From: address for outbound emails
	SMTPFromAddress string

	// RoutussyURL is the base URL of the Routussy proxy (e.g. "https://api.ussyco.de")
	RoutussyURL string

	// RoutussyInternalKey is the shared secret for authenticating to Routussy internal API endpoints
	RoutussyInternalKey string
}

// DefaultConfig returns a Config with sensible defaults.
// Environment variables are checked first; if unset, hardcoded defaults are used.
func DefaultConfig() *Config {
	dataDir := envOrDefault("USSYCODE_DATA_DIR", "/var/lib/ussycode")
	return &Config{
		Domain:              envOrDefault("USSYCODE_DOMAIN", "ussy.host"),
		SSHListenAddr:       envOrDefault("USSYCODE_SSH_ADDR", ":2222"),
		SSHHostKeyPath:      envOrDefault("USSYCODE_SSH_HOST_KEY", filepath.Join(dataDir, "ssh_host_ed25519_key")),
		HTTPListenAddr:      envOrDefault("USSYCODE_HTTP_ADDR", ":8080"),
		DataDir:             dataDir,
		DBPath:              envOrDefault("USSYCODE_DB_PATH", filepath.Join(dataDir, "ussycode.db")),
		CaddyAdminAddr:      envOrDefault("USSYCODE_CADDY_ADMIN", "http://localhost:2019"),
		MetadataAddr:        envOrDefault("USSYCODE_METADATA_ADDR", ":8083"),
		AuthProxyAddr:       envOrDefault("USSYCODE_AUTH_PROXY_ADDR", ":9876"),
		AuthProxyURL:        envOrDefault("USSYCODE_AUTH_PROXY_URL", "http://localhost:9876"),
		VMM:                 envOrDefault("USSYCODE_VMM", "firecracker"),
		KernelPath:          envOrDefault("USSYCODE_KERNEL", filepath.Join(dataDir, "vmlinux")),
		DefaultImage:        envOrDefault("USSYCODE_DEFAULT_IMAGE", "ussyuntu"),
		StorageBackend:      envOrDefault("USSYCODE_STORAGE", "lvm"),
		StoragePool:         envOrDefault("USSYCODE_STORAGE_POOL", "ussycode"),
		NetworkBridge:       envOrDefault("USSYCODE_BRIDGE", "ussy0"),
		NetworkSubnet:       envOrDefault("USSYCODE_SUBNET", "10.0.0.0/24"),
		MaxVMsPerUser:       envOrDefaultInt("USSYCODE_MAX_VMS", 5),
		DefaultCPU:          envOrDefaultInt("USSYCODE_DEFAULT_CPU", 1),
		DefaultMemoryMB:     envOrDefaultInt("USSYCODE_DEFAULT_MEM", 512),
		DefaultDiskGB:       envOrDefaultInt("USSYCODE_DEFAULT_DISK", 5),
		TLSEmail:            envOrDefault("USSYCODE_TLS_EMAIL", ""),
		DNSProvider:         envOrDefault("USSYCODE_DNS_PROVIDER", "cloudflare"),
		DNSAPIToken:         envOrDefault("USSYCODE_DNS_API_TOKEN", ""),
		Debug:               envOrDefault("USSYCODE_DEBUG", "") != "",
		FirecrackerBin:      envOrDefault("USSYCODE_FIRECRACKER_BIN", "firecracker"),
		AdminListenAddr:     envOrDefault("USSYCODE_ADMIN_ADDR", ":9090"),
		LLMEncryptSecret:    envOrDefault("USSYCODE_LLM_ENCRYPT_SECRET", ""),
		SMTPListenAddr:      envOrDefault("USSYCODE_SMTP_ADDR", ":2525"),
		SMTPRelay:           envOrDefault("USSYCODE_SMTP_RELAY", "localhost:25"),
		SMTPFromAddress:     envOrDefault("USSYCODE_SMTP_FROM", "noreply@ussy.host"),
		RoutussyURL:         envOrDefault("USSYCODE_ROUTUSSY_URL", ""),
		RoutussyInternalKey: envOrDefault("USSYCODE_ROUTUSSY_INTERNAL_KEY", ""),
	}
}

// RegisterFlags registers CLI flags that override the config values.
// Call flag.Parse() after this to apply flag overrides.
// Precedence: CLI flag (if set) > env var > hardcoded default.
func (c *Config) RegisterFlags(fs *flag.FlagSet) {
	fs.StringVar(&c.Domain, "domain", c.Domain, "Base domain for VM subdomains")
	fs.StringVar(&c.SSHListenAddr, "addr", c.SSHListenAddr, "SSH gateway listen address")
	fs.StringVar(&c.SSHHostKeyPath, "host-key", c.SSHHostKeyPath, "SSH host key file path")
	fs.StringVar(&c.HTTPListenAddr, "http-addr", c.HTTPListenAddr, "HTTP API listen address")
	fs.StringVar(&c.DataDir, "data-dir", c.DataDir, "Root data directory for VM runtime files")
	fs.StringVar(&c.DBPath, "db", c.DBPath, "SQLite database path")
	fs.StringVar(&c.CaddyAdminAddr, "caddy-api", c.CaddyAdminAddr, "Caddy admin API URL")
	fs.StringVar(&c.MetadataAddr, "metadata-addr", c.MetadataAddr, "Metadata service listen address")
	fs.StringVar(&c.AuthProxyAddr, "auth-proxy-addr", c.AuthProxyAddr, "Auth proxy listen address (for Caddy forward_auth)")
	fs.StringVar(&c.AuthProxyURL, "auth-proxy-url", c.AuthProxyURL, "Auth proxy URL Caddy uses for forward_auth (e.g. http://localhost:9876)")
	fs.StringVar(&c.VMM, "vmm", c.VMM, "Hypervisor backend: firecracker or cloudhv")
	fs.StringVar(&c.KernelPath, "kernel", c.KernelPath, "Path to guest kernel")
	fs.StringVar(&c.FirecrackerBin, "firecracker", c.FirecrackerBin, "Path to firecracker binary")
	fs.StringVar(&c.DefaultImage, "default-image", c.DefaultImage, "Default container image for new VMs")
	fs.StringVar(&c.StorageBackend, "storage", c.StorageBackend, "Storage backend: lvm or zfs")
	fs.StringVar(&c.StoragePool, "storage-pool", c.StoragePool, "LVM VG or ZFS pool name")
	fs.StringVar(&c.NetworkBridge, "bridge", c.NetworkBridge, "Bridge interface for VM networking")
	fs.StringVar(&c.NetworkSubnet, "subnet", c.NetworkSubnet, "CIDR subnet for VM IPs")
	fs.IntVar(&c.MaxVMsPerUser, "max-vms", c.MaxVMsPerUser, "Max VMs per user")
	fs.IntVar(&c.DefaultCPU, "default-cpu", c.DefaultCPU, "Default vCPU count per VM")
	fs.IntVar(&c.DefaultMemoryMB, "default-mem", c.DefaultMemoryMB, "Default memory in MB per VM")
	fs.IntVar(&c.DefaultDiskGB, "default-disk", c.DefaultDiskGB, "Default disk size in GB per VM")
	fs.StringVar(&c.TLSEmail, "acme-email", c.TLSEmail, "ACME email for TLS certificates")
	fs.StringVar(&c.DNSProvider, "dns-provider", c.DNSProvider, "DNS provider for wildcard cert challenges")
	fs.StringVar(&c.DNSAPIToken, "dns-api-token", c.DNSAPIToken, "DNS provider API token")
	fs.BoolVar(&c.Debug, "debug", c.Debug, "Enable debug logging")
	fs.StringVar(&c.AdminListenAddr, "admin-addr", c.AdminListenAddr, "Admin web panel listen address")
	fs.StringVar(&c.LLMEncryptSecret, "llm-encrypt-secret", c.LLMEncryptSecret, "Secret for encrypting stored LLM API keys")
	fs.StringVar(&c.SMTPListenAddr, "smtp-addr", c.SMTPListenAddr, "Inbound SMTP server listen address")
	fs.StringVar(&c.SMTPRelay, "smtp-relay", c.SMTPRelay, "Outbound SMTP relay address (host:port)")
	fs.StringVar(&c.SMTPFromAddress, "smtp-from", c.SMTPFromAddress, "From: address for outbound emails")
	fs.StringVar(&c.RoutussyURL, "routussy-url", c.RoutussyURL, "Routussy proxy base URL (e.g. https://api.ussyco.de)")
	fs.StringVar(&c.RoutussyInternalKey, "routussy-key", c.RoutussyInternalKey, "Shared secret for Routussy internal API")
}

// Validate checks that required configuration is present and consistent.
func (c *Config) Validate() error {
	if c.Domain == "" {
		return fmt.Errorf("config: domain is required (set USSYCODE_DOMAIN or -domain)")
	}
	if c.DataDir == "" {
		return fmt.Errorf("config: data directory is required (set USSYCODE_DATA_DIR or -data-dir)")
	}
	if c.SSHListenAddr == "" {
		return fmt.Errorf("config: SSH listen address is required (set USSYCODE_SSH_ADDR or -addr)")
	}
	if c.DBPath == "" {
		return fmt.Errorf("config: database path is required (set USSYCODE_DB_PATH or -db)")
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
