package main

import (
	"context"
	"flag"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/mojomast/exedevussy/internal/db"
	"github.com/mojomast/exedevussy/internal/gateway"
	"github.com/mojomast/exedevussy/internal/proxy"
	sshgw "github.com/mojomast/exedevussy/internal/ssh"
	"github.com/mojomast/exedevussy/internal/vm"
)

func main() {
	addr := flag.String("addr", ":2222", "SSH listen address")
	dbPath := flag.String("db", "exedevussy.db", "SQLite database path")
	hostKey := flag.String("host-key", "host_key", "SSH host key file path")
	domain := flag.String("domain", "ussy.host", "Domain for VM URLs")
	dataDir := flag.String("data-dir", "/var/lib/exedevussy", "Data directory for VM runtime files")
	firecrackerBin := flag.String("firecracker", "firecracker", "Path to firecracker binary")
	kernelPath := flag.String("kernel", "/var/lib/exedevussy/vmlinux", "Path to guest kernel")
	bridge := flag.String("bridge", "ussy0", "Bridge interface for VM networking")
	subnet := flag.String("subnet", "10.0.0.0/24", "CIDR subnet for VM IPs")
	metadataAddr := flag.String("metadata-addr", "169.254.169.254:80", "Metadata service listen address")
	caddyAPI := flag.String("caddy-api", "http://localhost:2019", "Caddy admin API URL")
	authProxyAddr := flag.String("auth-proxy-addr", ":9876", "Auth proxy listen address (for Caddy forward_auth)")
	acmeEmail := flag.String("acme-email", "", "ACME email for TLS certificates")
	debug := flag.Bool("debug", false, "Enable debug logging")
	flag.Parse()

	// Setup structured logger
	logLevel := slog.LevelInfo
	if *debug {
		logLevel = slog.LevelDebug
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: logLevel}))
	slog.SetDefault(logger)

	log.SetFlags(log.LstdFlags | log.Lshortfile)
	log.SetOutput(os.Stderr)

	// Open database
	database, err := db.Open(*dbPath)
	if err != nil {
		log.Fatalf("open database: %v", err)
	}
	defer database.Close()

	// Run migrations
	ctx := context.Background()
	if err := database.Migrate(ctx); err != nil {
		log.Fatalf("migrate database: %v", err)
	}
	log.Println("database migrated")

	// Try to initialize VM manager (optional -- may fail if firecracker is not installed)
	var vmManager *vm.Manager
	vmManager, err = vm.NewManager(database, &vm.ManagerConfig{
		DataDir:        *dataDir,
		FirecrackerBin: *firecrackerBin,
		KernelPath:     *kernelPath,
		BridgeName:     *bridge,
		SubnetCIDR:     *subnet,
	}, logger.With("component", "vm"))
	if err != nil {
		log.Printf("WARNING: VM manager unavailable: %v", err)
		log.Println("SSH gateway will start without VM provisioning support.")
		log.Println("VMs will be created as DB records only (status: stopped).")
		vmManager = nil
	}

	// Initialize metadata service
	metaSrv := gateway.NewServer(*metadataAddr, logger.With("component", "metadata"))

	// Initialize proxy manager (optional -- needs Caddy running)
	var proxyMgr *proxy.Manager
	proxyMgr = proxy.NewManager(&proxy.Config{
		AdminAPI: *caddyAPI,
		Domain:   *domain,
	}, logger.With("component", "proxy"))

	// Try to configure Caddy base config (non-fatal if Caddy isn't running)
	if proxyMgr.Healthy(ctx) {
		if *acmeEmail != "" {
			if err := proxyMgr.EnsureBaseConfig(ctx, *acmeEmail); err != nil {
				log.Printf("WARNING: failed to configure Caddy: %v", err)
			} else {
				log.Println("Caddy base config loaded")
			}
		}
		log.Println("Caddy proxy: connected")
	} else {
		log.Println("WARNING: Caddy admin API not reachable at", *caddyAPI)
		log.Println("HTTPS proxy routes will be configured but may not take effect until Caddy starts.")
	}

	// Create auth proxy (for Caddy forward_auth)
	authProxy := proxy.NewAuthProxy(database, *domain, logger.With("component", "auth-proxy"))

	// Create SSH gateway
	gw, err := sshgw.New(database, vmManager, metaSrv, proxyMgr, *hostKey, *addr, *domain)
	if err != nil {
		log.Fatalf("create gateway: %v", err)
	}

	// Graceful shutdown context
	shutdownCtx, shutdownCancel := context.WithCancel(ctx)
	defer shutdownCancel()

	// Start metadata service in background
	go func() {
		if err := metaSrv.Start(shutdownCtx); err != nil {
			log.Printf("metadata server error: %v", err)
		}
	}()

	// Start auth proxy HTTP server in background
	authSrv := &http.Server{
		Addr:    *authProxyAddr,
		Handler: authProxy.Handler(),
	}
	go func() {
		log.Printf("auth proxy listening on %s", *authProxyAddr)
		if err := authSrv.ListenAndServe(); err != http.ErrServerClosed {
			log.Printf("auth proxy error: %v", err)
		}
	}()

	// Start SSH gateway
	done := make(chan os.Signal, 1)
	signal.Notify(done, os.Interrupt, syscall.SIGTERM)

	go func() {
		if err := gw.ListenAndServe(); err != nil {
			log.Fatalf("ssh server: %v", err)
		}
	}()

	log.Printf("exedevussy started on %s (domain: %s)", *addr, *domain)
	if vmManager != nil {
		log.Println("VM provisioning: enabled")
	} else {
		log.Println("VM provisioning: disabled (no firecracker)")
	}

	<-done
	log.Println("shutting down...")

	// Shutdown VM manager first (stops all running VMs)
	if vmManager != nil {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 30*time.Second)
		vmManager.ShutdownAll(stopCtx)
		stopCancel()
	}

	// Cancel metadata service
	shutdownCancel()

	// Shutdown auth proxy
	authShutCtx, authShutCancel := context.WithTimeout(context.Background(), 5*time.Second)
	authSrv.Shutdown(authShutCtx)
	authShutCancel()

	// Shutdown SSH gateway
	gwCtx, gwCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer gwCancel()

	if err := gw.Shutdown(gwCtx); err != nil {
		log.Printf("shutdown error: %v", err)
	}

	log.Println("goodbye.")
}
