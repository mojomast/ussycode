package config

import (
	"flag"
	"os"
	"testing"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.Domain != "ussy.host" {
		t.Errorf("Domain = %q, want %q", cfg.Domain, "ussy.host")
	}
	if cfg.SSHListenAddr != ":2222" {
		t.Errorf("SSHListenAddr = %q, want %q", cfg.SSHListenAddr, ":2222")
	}
	if cfg.MetadataAddr != ":8083" {
		t.Errorf("MetadataAddr = %q, want %q", cfg.MetadataAddr, ":8083")
	}
	if cfg.AuthProxyAddr != ":9876" {
		t.Errorf("AuthProxyAddr = %q, want %q", cfg.AuthProxyAddr, ":9876")
	}
	if cfg.NetworkBridge != "ussy0" {
		t.Errorf("NetworkBridge = %q, want %q", cfg.NetworkBridge, "ussy0")
	}
	if cfg.FirecrackerBin != "firecracker" {
		t.Errorf("FirecrackerBin = %q, want %q", cfg.FirecrackerBin, "firecracker")
	}
	if cfg.MaxVMsPerUser != 5 {
		t.Errorf("MaxVMsPerUser = %d, want %d", cfg.MaxVMsPerUser, 5)
	}
	if cfg.Debug != false {
		t.Errorf("Debug = %v, want false", cfg.Debug)
	}
}

func TestDefaultConfig_EnvOverride(t *testing.T) {
	// Set env var and verify it takes effect
	os.Setenv("USSYCODE_DOMAIN", "test.example.com")
	defer os.Unsetenv("USSYCODE_DOMAIN")

	os.Setenv("USSYCODE_DEBUG", "1")
	defer os.Unsetenv("USSYCODE_DEBUG")

	os.Setenv("USSYCODE_MAX_VMS", "10")
	defer os.Unsetenv("USSYCODE_MAX_VMS")

	cfg := DefaultConfig()

	if cfg.Domain != "test.example.com" {
		t.Errorf("Domain = %q, want %q", cfg.Domain, "test.example.com")
	}
	if cfg.Debug != true {
		t.Errorf("Debug = %v, want true", cfg.Debug)
	}
	if cfg.MaxVMsPerUser != 10 {
		t.Errorf("MaxVMsPerUser = %d, want %d", cfg.MaxVMsPerUser, 10)
	}
}

func TestRegisterFlags(t *testing.T) {
	cfg := DefaultConfig()
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	cfg.RegisterFlags(fs)

	// Parse with flag overrides
	err := fs.Parse([]string{
		"-domain", "flag.example.com",
		"-addr", ":3333",
		"-debug",
		"-max-vms", "20",
		"-firecracker", "/usr/local/bin/firecracker",
	})
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	if cfg.Domain != "flag.example.com" {
		t.Errorf("Domain = %q, want %q", cfg.Domain, "flag.example.com")
	}
	if cfg.SSHListenAddr != ":3333" {
		t.Errorf("SSHListenAddr = %q, want %q", cfg.SSHListenAddr, ":3333")
	}
	if cfg.Debug != true {
		t.Errorf("Debug = %v, want true", cfg.Debug)
	}
	if cfg.MaxVMsPerUser != 20 {
		t.Errorf("MaxVMsPerUser = %d, want %d", cfg.MaxVMsPerUser, 20)
	}
	if cfg.FirecrackerBin != "/usr/local/bin/firecracker" {
		t.Errorf("FirecrackerBin = %q, want %q", cfg.FirecrackerBin, "/usr/local/bin/firecracker")
	}
}

func TestRegisterFlags_FlagOverridesEnv(t *testing.T) {
	// Env sets domain to one value, flag overrides to another
	os.Setenv("USSYCODE_DOMAIN", "env.example.com")
	defer os.Unsetenv("USSYCODE_DOMAIN")

	cfg := DefaultConfig()
	if cfg.Domain != "env.example.com" {
		t.Fatalf("Domain after env = %q, want %q", cfg.Domain, "env.example.com")
	}

	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	cfg.RegisterFlags(fs)
	err := fs.Parse([]string{"-domain", "flag.example.com"})
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	if cfg.Domain != "flag.example.com" {
		t.Errorf("Domain = %q, want %q (flag should override env)", cfg.Domain, "flag.example.com")
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(c *Config)
		wantErr bool
	}{
		{
			name:    "valid defaults",
			mutate:  func(c *Config) {},
			wantErr: false,
		},
		{
			name:    "missing domain",
			mutate:  func(c *Config) { c.Domain = "" },
			wantErr: true,
		},
		{
			name:    "missing data dir",
			mutate:  func(c *Config) { c.DataDir = "" },
			wantErr: true,
		},
		{
			name:    "missing SSH addr",
			mutate:  func(c *Config) { c.SSHListenAddr = "" },
			wantErr: true,
		},
		{
			name:    "missing DB path",
			mutate:  func(c *Config) { c.DBPath = "" },
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultConfig()
			tt.mutate(cfg)
			err := cfg.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr = %v", err, tt.wantErr)
			}
		})
	}
}
