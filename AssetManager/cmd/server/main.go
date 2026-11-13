// AssetManager
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Command assetmanager is the AssetManager service entry point. It loads
// configuration, sets up logging, opens the SQLite database and runs its
// migrations, starts the WebSocket hub, and serves the HTTP API on the
// configured port until it receives SIGINT/SIGTERM.
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"tachyonik/assetmanager/internal/config"
	"tachyonik/assetmanager/internal/database"
	"tachyonik/assetmanager/internal/handlers"
	"tachyonik/assetmanager/internal/rscmanager"
	"tachyonik/assetmanager/internal/systemmanager"
	"tachyonik/assetmanager/internal/version"
	"tachyonik/assetmanager/internal/websocket"
	"tachyonik/lib/logger"
)

func setupLogging(cfg *config.Config) (*os.File, error) {
	return logger.SetupFromOptions(logger.FileOptions{
		ToConsole: cfg.Log.ToConsole,
		ToFile:    cfg.Log.ToFile,
		FilePath:  cfg.Log.FilePath,
		Level:     cfg.Log.Level,
	})
}

func main() {
	// Route subcommand. Neither needs the configuration, so neither runs
	// after it. Flags are accepted as well as bare words, as in the daemons:
	// `assetmanager version` and `assetmanager --version` both work.
	for _, arg := range os.Args[1:] {
		switch arg {
		case "help", "--help", "-h":
			runHelp()
			return
		case "version", "--version", "-v":
			fmt.Printf("Tachyonik AssetManager %s\n", version.Version)
			return
		}
	}

	// Load configuration
	cfg, err := config.Load()
	if err != nil {
		// Before logging is set up: standard error reaches journalctl and
		// docker logs. Running on defaults instead would be worse (AM-OPS-16).
		fmt.Fprintf(os.Stderr, "AssetManager cannot start: %v\n", err)
		os.Exit(1)
	}

	// Setup logging based on configuration
	logFile, err := setupLogging(cfg)
	if err != nil {
		logger.Fatalf("Failed to setup logging: %v", err)
	}
	if logFile != nil {
		defer logFile.Close()
	}

	logger.Infof("Starting Tachyonik AssetManager server %s...", version.Version)
	logger.Info("Configuration loaded:")
	logger.Infof("  Database Path: %s", cfg.Database.Path)
	logger.Infof("  Server: %s:%s", cfg.Server.Host, cfg.Server.Port)
	logger.Infof("  Log File: %s", cfg.Log.FilePath)
	logger.Infof("  Log to Console: %v", cfg.Log.ToConsole)
	logger.Infof("  Log to File: %v", cfg.Log.ToFile)
	logger.Infof("  Log Level: %s", cfg.Log.Level)
	// Log the effective WebSocket CORS allow-list. A browser whose Origin is
	// not on this list has its /ws upgrade rejected, which surfaces as the
	// service showing "Disconnected" in the health overview. An empty or
	// localhost-only list in a non-local deployment is the usual cause.
	logger.Infof("  WebSocket Allowed Origins: %v", cfg.CORS.AllowedOrigins)
	// Started regardless, as a development setup runs on the placeholder; in
	// a deployment this is the line to act on. It must match SystemManager's.
	if problem := cfg.JWTSecretProblem(); problem != "" {
		logger.Warnf("SECURITY: %s — set a secret of at least %d random characters, the same as SystemManager's", problem, config.MinJWTSecretLength)
	}

	// Initialize database
	db, err := database.New(cfg.Database.Path)
	if err != nil {
		logger.Fatalf("Failed to initialize database: %v", err)
	}
	defer db.Close()

	// Run migrations
	if err := db.Migrate(); err != nil {
		logger.Fatalf("Failed to run migrations: %v", err)
	}
	logger.Info("Database migrations completed")

	// Assets stored before the types were a fixed set stay as they are;
	// say so, as nothing type-specific treats them.
	if unknown, err := db.AssetsOfUnknownType(); err != nil {
		logger.Warnf("Could not check for assets of unknown type: %v", err)
	} else {
		for t, n := range unknown {
			logger.Warnf("%d asset(s) of unknown type %q are stored (known: %s); they are kept but no longer accepted", n, t, strings.Join(database.AssetTypes, ", "))
		}
	}

	// Initialize WebSocket hub
	hub := websocket.NewHub()
	go hub.Run()
	logger.Info("WebSocket hub started")

	// Initialize ResourceManager API client
	rscManagerClient := rscmanager.NewClient(cfg.ResourceManager.URL, cfg.ResourceManager.InternalServiceKey)
	logger.Info("ResourceManager API client initialized")

	// Initialize SystemManager API client
	systemManagerClient := systemmanager.NewClient(cfg.SystemManager.URL, cfg.SystemManager.InternalServiceKey)
	logger.Info("SystemManager API client initialized")

	// Initialize handlers
	handler := handlers.NewHandler(db, hub, cfg, rscManagerClient, systemManagerClient)
	router := handler.SetupRoutes()

	// Recompute every asset from its evidence (severity, score, last seen,
	// last scanned — see database/asset_refresh.go), then the organisation
	// scores. Puts right whatever an older version left behind.
	go func() {
		changed, err := db.RefreshAllAssets()
		if err != nil {
			logger.Errorf("Startup: failed to recompute assets: %v", err)
		} else {
			logger.Infof("Startup: recomputed assets, %d changed", len(changed))
		}
		handler.RecalculateAllOrganisationScores()
	}()

	// Create HTTP server
	addr := cfg.Server.Host + ":" + cfg.Server.Port
	server := &http.Server{
		Addr:         addr,
		Handler:      router,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// Flip the readiness gate now — every prerequisite above is wired.
	// The score-recalculation goroutine runs asynchronously and isn't
	// blocking for serving requests (it touches an existing populated
	// table; clients can wait for it transitively).
	handler.MarkReady()

	// Start server in a goroutine
	go func() {
		logger.Infof("Server listening on http://%s", addr)
		logger.Infof("WebSocket endpoint: ws://%s/ws", addr)
		logger.Infof("API endpoint: http://%s/api/assets", addr)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Fatalf("Server failed: %v", err)
		}
	}()

	// Wait for interrupt signal to gracefully shutdown the server
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	logger.Info("Shutting down server...")

	// Give outstanding requests 10 seconds to complete
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := server.Shutdown(ctx); err != nil {
		logger.Fatalf("Server forced to shutdown: %v", err)
	}

	// Emit any coalesced limit-exceeded audit summaries still buffered.
	handler.Shutdown()

	logger.Info("Server stopped")
}

func runHelp() {
	fmt.Println("Tachyonik AssetManager")
	fmt.Println()
	fmt.Println("Usage: assetmanager [command]")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  (none)      Start the AssetManager server (default)")
	fmt.Println("  help        Show this help message")
	fmt.Println("  version     Print the build version and exit")
}
