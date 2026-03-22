package main

import (
	"context"
	"flag"
	"io/fs"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/mojomast/ussycode/internal/admin"
	"github.com/mojomast/ussycode/internal/api"
	"github.com/mojomast/ussycode/internal/config"
	"github.com/mojomast/ussycode/internal/db"
	"github.com/mojomast/ussycode/internal/gateway"
	"github.com/mojomast/ussycode/internal/proxy"
	sshgw "github.com/mojomast/ussycode/internal/ssh"
	"github.com/mojomast/ussycode/internal/telemetry"
	"github.com/mojomast/ussycode/internal/vm"
	"golang.org/x/crypto/ssh"
)

func main() {
	// Load config: env vars -> defaults, then register CLI flags for overrides.
	cfg := config.DefaultConfig()
	cfg.RegisterFlags(flag.CommandLine)
	flag.Parse()

	if err := cfg.Validate(); err != nil {
		log.Fatalf("invalid configuration: %v", err)
	}

	// Setup structured logger
	logLevel := slog.LevelInfo
	if cfg.Debug {
		logLevel = slog.LevelDebug
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: logLevel}))
	slog.SetDefault(logger)

	telemetryShutdown, err := telemetry.Setup(context.Background(), "ussycode", api.Version, logger.With("component", "telemetry"))
	if err != nil {
		logger.Warn("telemetry setup failed; continuing without OTLP export", "error", err)
	}
	defer telemetryShutdown(context.Background())

	log.SetFlags(log.LstdFlags | log.Lshortfile)
	log.SetOutput(os.Stderr)

	// Open database
	database, err := db.Open(cfg.DBPath)
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
		DataDir:        cfg.DataDir,
		FirecrackerBin: cfg.FirecrackerBin,
		KernelPath:     cfg.KernelPath,
		BridgeName:     cfg.NetworkBridge,
		SubnetCIDR:     cfg.NetworkSubnet,
	}, logger.With("component", "vm"))
	if err != nil {
		log.Printf("WARNING: VM manager unavailable: %v", err)
		log.Println("SSH gateway will start without VM provisioning support.")
		log.Println("VMs will be created as DB records only (status: stopped).")
		vmManager = nil
	}

	// Initialize metadata service
	metaSrv := gateway.NewServer(cfg.MetadataAddr, logger.With("component", "metadata"))

	// Initialize LLM gateway (optional -- needs encrypt secret for BYOK)
	llmCfg := gateway.DefaultLLMGatewayConfig()
	if cfg.LLMEncryptSecret != "" {
		llmCfg.EncryptSecret = cfg.LLMEncryptSecret
	}
	llmGW, err := gateway.NewLLMGateway(database, llmCfg, logger.With("component", "llm"))
	if err != nil {
		log.Printf("WARNING: LLM gateway unavailable: %v", err)
	} else {
		metaSrv.SetLLMGateway(llmGW)
		log.Println("LLM gateway: enabled")
	}

	// Initialize outbound email sender (optional)
	emailSender := gateway.NewEmailSender(database, &gateway.EmailSendConfig{
		SMTPRelay:   cfg.SMTPRelay,
		FromAddress: cfg.SMTPFromAddress,
	}, logger.With("component", "email-send"))
	metaSrv.SetEmailSender(emailSender)
	log.Println("email sender: enabled")

	// Initialize inbound SMTP server (optional)
	smtpSrv := gateway.NewSMTPServer(&gateway.SMTPConfig{
		ListenAddr: cfg.SMTPListenAddr,
		Domain:     cfg.Domain,
		RootfsDir:  cfg.DataDir + "/disks",
	}, logger.With("component", "smtp"))

	// Initialize proxy manager (optional -- needs Caddy running)
	proxyMgr := proxy.NewManager(&proxy.Config{
		AdminAPI:     cfg.CaddyAdminAddr,
		Domain:       cfg.Domain,
		AuthProxyURL: cfg.AuthProxyURL,
	}, logger.With("component", "proxy"))
	if cfg.CaddyAdminAddr == "" {
		log.Println("WARNING: Caddy admin API disabled; browser routes will not be created.")
	}

	// Try to configure Caddy base config (non-fatal if Caddy isn't running)
	if proxyMgr.Healthy(ctx) {
		if cfg.TLSEmail != "" {
			if err := proxyMgr.EnsureBaseConfig(ctx, cfg.TLSEmail); err != nil {
				log.Printf("WARNING: failed to configure Caddy: %v", err)
			} else {
				log.Println("Caddy base config loaded")
			}
		}
		log.Println("Caddy proxy: connected")
	} else {
		log.Println("WARNING: Caddy admin API not reachable at", cfg.CaddyAdminAddr)
		log.Println("HTTPS proxy routes will be configured but may not take effect until Caddy starts.")
	}

	// Create auth proxy (for Caddy forward_auth)
	authProxy := proxy.NewAuthProxy(database, cfg.Domain, logger.With("component", "auth-proxy"))

	// Create SSH gateway
	gw, err := sshgw.New(database, vmManager, metaSrv, proxyMgr, cfg.SSHHostKeyPath, cfg.SSHListenAddr, cfg.Domain)
	if err != nil {
		log.Fatalf("create gateway: %v", err)
	}
	// Wire LLM gateway into SSH gateway so llm-key command works
	if llmGW != nil {
		gw.LLMGateway = llmGW
	}
	// Wire Routussy integration for SSH key validation and API key injection
	if cfg.RoutussyURL != "" {
		gw.RoutussyURL = cfg.RoutussyURL
		gw.RoutussyInternalKey = cfg.RoutussyInternalKey
		log.Printf("Routussy integration enabled: %s", cfg.RoutussyURL)
	}

	// Initialize API handler using the same command surface as the SSH gateway.
	apiExecutor := sshgw.NewAPIExecutor(gw)
	keyResolver := func(ctx context.Context, userID int64) ([]ssh.PublicKey, error) {
		keys, err := database.SSHKeysByUser(ctx, userID)
		if err != nil {
			return nil, err
		}
		resolved := make([]ssh.PublicKey, 0, len(keys))
		for _, key := range keys {
			pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(key.PublicKey))
			if err != nil {
				logger.Warn("skipping invalid SSH key during API auth", "user_id", userID, "key_id", key.ID, "error", err)
				continue
			}
			resolved = append(resolved, pub)
		}
		return resolved, nil
	}
	apiHandler := api.NewHandler(database, apiExecutor, keyResolver, logger.With("component", "api"), &api.Config{})

	// Initialize admin web panel
	webFS, err := fs.Sub(admin.WebFS, "web")
	if err != nil {
		log.Fatalf("admin web assets: %v", err)
	}
	adminHandler, err := admin.NewHandler(database, webFS, logger.With("component", "admin"), &admin.Config{
		Domain: cfg.Domain,
	})
	if err != nil {
		log.Fatalf("create admin panel: %v", err)
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

	// Start inbound SMTP server in background
	go func() {
		log.Printf("SMTP server listening on %s", cfg.SMTPListenAddr)
		if err := smtpSrv.Start(shutdownCtx); err != nil {
			log.Printf("SMTP server error: %v", err)
		}
	}()

	// Start auth proxy HTTP server in background
	authSrv := &http.Server{
		Addr:    cfg.AuthProxyAddr,
		Handler: authProxy.Handler(),
	}
	go func() {
		log.Printf("auth proxy listening on %s", cfg.AuthProxyAddr)
		if err := authSrv.ListenAndServe(); err != http.ErrServerClosed {
			log.Printf("auth proxy error: %v", err)
		}
	}()

	// Start HTTP API server in background
	httpMux := http.NewServeMux()
	apiHandler.Routes(httpMux)
	// Register the magic-link auth endpoint on the public HTTP mux so that
	// /__auth/magic/<token> URLs generated by the SSH `browser` command are
	// reachable through Caddy on the base domain (e.g. https://ussy.host).
	// The same route is also registered on the admin-panel mux via
	// adminHandler.Routes, but this copy ensures it works regardless of
	// which backend Caddy routes base-domain traffic to.
	httpMux.HandleFunc("GET /__auth/magic/{token}", adminHandler.HandleMagicLink)
	httpSrv := &http.Server{
		Addr:    cfg.HTTPListenAddr,
		Handler: httpMux,
	}
	go func() {
		log.Printf("HTTP API listening on %s", cfg.HTTPListenAddr)
		if err := httpSrv.ListenAndServe(); err != http.ErrServerClosed {
			log.Printf("HTTP API error: %v", err)
		}
	}()

	// Start admin web panel in background
	adminMux := http.NewServeMux()
	adminHandler.Routes(adminMux)
	adminSrv := &http.Server{
		Addr:    cfg.AdminListenAddr,
		Handler: adminMux,
	}
	go func() {
		log.Printf("admin panel listening on %s", cfg.AdminListenAddr)
		if err := adminSrv.ListenAndServe(); err != http.ErrServerClosed {
			log.Printf("admin panel error: %v", err)
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

	log.Printf("ussycode started on %s (domain: %s)", cfg.SSHListenAddr, cfg.Domain)
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

	// Cancel metadata + SMTP services
	shutdownCancel()

	// Shutdown HTTP servers gracefully
	httpShutCtx, httpShutCancel := context.WithTimeout(context.Background(), 5*time.Second)
	httpSrv.Shutdown(httpShutCtx)
	httpShutCancel()

	adminShutCtx, adminShutCancel := context.WithTimeout(context.Background(), 5*time.Second)
	adminSrv.Shutdown(adminShutCtx)
	adminShutCancel()

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
