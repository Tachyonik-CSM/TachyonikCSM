// TachyonikProxy
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"os/user"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"tachyonik/lib/logger"
	"tachyonik/tachyonikproxy/internal/config"
	"tachyonik/tachyonikproxy/internal/enroll"
	"tachyonik/tachyonikproxy/internal/mcpserver"
	"tachyonik/tachyonikproxy/internal/netscan"
	"tachyonik/tachyonikproxy/internal/reverseconnect"
	"tachyonik/tachyonikproxy/internal/selfupdate"
	"tachyonik/tachyonikproxy/internal/tlsutil"
	"tachyonik/tachyonikproxy/internal/tools"
	"tachyonik/tachyonikproxy/internal/toolscan"
)

// Set via -ldflags at build time.
var version = "dev"

func main() {
	// Scan all arguments for a subcommand (flags like --insecure may appear before it).
	for _, arg := range os.Args[1:] {
		if strings.HasPrefix(arg, "-") {
			continue // skip flags
		}
		switch arg {
		case "help":
			runHelp()
			return
		case "version":
			fmt.Printf("Tachyonik TachyonikProxy %s\n", version)
			return
		case "enroll":
			runEnroll()
			return
		case "reset-enrollment":
			runResetEnrollment()
			return
		case "scan":
			runScan()
			return
		case "netscan":
			runNetScan()
			return
		case "self-update":
			runSelfUpdate()
			return
		}
		break // first non-flag arg that isn't a known subcommand → run server
	}

	// Handle --help / --version flags without a subcommand
	for _, arg := range os.Args[1:] {
		switch arg {
		case "--help", "-h":
			runHelp()
			return
		case "--version", "-v":
			fmt.Printf("Tachyonik TachyonikProxy %s\n", version)
			return
		}
	}

	// Platform-specific entry point: on Windows this checks whether the
	// process was started by the Service Control Manager and, if so,
	// runs as a Windows service.  On other platforms it calls runServer()
	// directly.
	platformMain()
}

// runServer contains the core server logic, called from platformMain().
func runServer() {
	cfg := config.Load()

	logFile := setupLogging(cfg)
	if logFile != nil {
		defer logFile.Close()
	}

	logger.Infof("Starting Tachyonik TachyonikProxy '%s' (version %s)", cfg.Proxy.Name, version)
	logger.Infof("Description: %s", cfg.Proxy.Description)
	logAutoUpdateStatus()

	// Initialize tool registry
	registry := tools.NewRegistry(cfg.Tools, cfg.MCPServers)
	logger.Infof("Loaded %d local tool(s)", len(cfg.Tools))

	// Start the local-network sweep before the MCP server, so a scan request
	// arriving early finds a scanner that at least reports itself not-ready
	// rather than absent. Its goroutine is independent of request handling:
	// sweeps never block the MCP server, and routines only ever read the
	// cached snapshot.
	netScanner := startNetScan(cfg)

	// Initialize MCP server
	mcpSrv := mcpserver.NewServer(cfg, registry, version, netScanner)

	// Tools execute as this process's user. Running the proxy as root means
	// every tool (and every scan routine's exec()) runs as root too, turning
	// any tool-side flaw into host compromise. Warn loudly; recommend a
	// dedicated unprivileged service account. (Geteuid returns -1 on Windows,
	// so this only fires on unix.)
	if os.Geteuid() == 0 {
		logger.Warnf("SECURITY: TachyonikProxy is running as root; executed tools will also run as root. Run it as a dedicated unprivileged user instead.")
	}

	var server *http.Server
	var dialer *reverseconnect.Dialer

	if cfg.ConnectionMode == "inbound" {
		// Mode 2: Reverse-connect — dial ToolManager via WebSocket
		requireInboundTLS(cfg)

		dialer = reverseconnect.NewDialer(cfg, mcpSrv)
		dialer.Start()
		logger.Infof("Running in inbound (reverse-connect) mode")

		// Optionally start a localhost-only health endpoint for local debugging
		healthMux := http.NewServeMux()
		healthMux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"status":"ok","mode":"inbound"}`))
		})
		healthAddr := "127.0.0.1:" + cfg.Server.Port
		server = &http.Server{
			Addr:    healthAddr,
			Handler: healthMux,
		}
		go func() {
			logger.Infof("Health endpoint on http://%s/health", healthAddr)
			if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				logger.Warnf("Health server failed: %v", err)
			}
		}()
	} else {
		// Mode 1: Outbound — start HTTPS+mTLS server. Plain HTTP is not
		// supported: an externally-reachable listener must always be TLS.
		requireOutboundTLS(cfg)

		transport := mcpserver.NewSSETransport(mcpSrv)

		mux := http.NewServeMux()
		mux.HandleFunc("/mcp/sse", transport.HandleSSE)
		mux.HandleFunc("/mcp/message", transport.HandleMessage)
		mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"status":"ok","mode":"outbound"}`))
		})

		addr := cfg.Server.Host + ":" + cfg.Server.Port

		// Resolve relative cert paths against the config dir before
		// handing them to the TLS loader (the stored config keeps its
		// original values so the bundle stays relocatable).
		tlsPaths := cfg.TLS
		tlsPaths.CACert = cfg.AbsPath(tlsPaths.CACert)
		tlsPaths.ServerCert = cfg.AbsPath(tlsPaths.ServerCert)
		tlsPaths.ServerKey = cfg.AbsPath(tlsPaths.ServerKey)
		tlsConfig, err := tlsutil.LoadServerTLSConfig(&tlsPaths)
		if err != nil {
			logger.Fatalf("Failed to load TLS config: %v", err)
		}

		server = &http.Server{
			Addr:         addr,
			Handler:      mux,
			TLSConfig:    tlsConfig,
			ReadTimeout:  15 * time.Second,
			WriteTimeout: 600 * time.Second,
			IdleTimeout:  600 * time.Second,
		}

		go func() {
			logger.Infof("TachyonikProxy listening on https://%s (mTLS enabled)", addr)
			logger.Infof("Allowed clients: %v", cfg.TLS.AllowedClients)
			logger.Infof("MCP SSE endpoint: https://%s/mcp/sse", addr)
			logger.Infof("MCP message endpoint: https://%s/mcp/message", addr)
			if err := server.ListenAndServeTLS("", ""); err != nil && err != http.ErrServerClosed {
				logger.Fatalf("Server failed: %v", err)
			}
		}()
	}

	// Wait for interrupt signal
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	logger.Infof("Shutting down TachyonikProxy...")

	if dialer != nil {
		dialer.Stop()
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if server != nil {
		if err := server.Shutdown(ctx); err != nil {
			logger.Fatalf("Server forced to shutdown: %v", err)
		}
	}

	logger.Infof("TachyonikProxy stopped")
}

// requireOutboundTLS enforces that the proxy refuses to start in outbound mode
// without a complete TLS configuration. The outbound listener is externally
// reachable; plain HTTP is not allowed under any circumstance. Two failure
// modes are distinguished so the operator gets the right hint:
//   - All three TLS paths empty → proxy was never enrolled.
//   - Some set, some missing → config is broken and should be reset.
func requireOutboundTLS(cfg *config.Config) {
	t := &cfg.TLS
	type field struct {
		name string
		path string
	}
	fields := []field{
		{"ca_cert", t.CACert},
		{"server_cert", t.ServerCert},
		{"server_key", t.ServerKey},
	}

	var missing []string
	for _, f := range fields {
		if f.path == "" {
			missing = append(missing, f.name)
		}
	}

	if len(missing) == len(fields) {
		logger.Fatalf("TLS not configured — proxy is not enrolled. " +
			"Run: tachyonikproxy enroll <enrollment-url>  (or)  tachyonikproxy enroll --listen")
	}
	if len(missing) > 0 {
		logger.Fatalf("TLS configuration is incomplete (missing: %s). "+
			"Run 'tachyonikproxy reset-enrollment' and re-enroll.", strings.Join(missing, ", "))
	}

	for _, f := range fields {
		requireTLSFileReadable(f.name, cfg.AbsPath(f.path))
	}
}

// requireInboundTLS is requireOutboundTLS for reverse-connect mode. Without it
// unreadable client cert material produced no startup check at all: the dialer
// reported "failed to load client certificate: ... permission denied" and
// retried forever, so systemd counted restarts while the actual cause — the
// cert directory belonging to the wrong account — never appeared in the log.
func requireInboundTLS(cfg *config.Config) {
	fields := []struct{ name, path string }{
		{"ca_cert", cfg.TLS.CACert},
		{"reverse_connect.client_cert", cfg.ReverseConnect.ClientCert},
		{"reverse_connect.client_key", cfg.ReverseConnect.ClientKey},
	}

	var missing []string
	for _, f := range fields {
		if f.path == "" {
			missing = append(missing, f.name)
		}
	}
	if len(missing) == len(fields) {
		logger.Fatalf("TLS not configured — proxy is not enrolled. " +
			"Run: tachyonikproxy enroll <enrollment-url>  (or)  tachyonikproxy enroll --listen")
	}
	if len(missing) > 0 {
		logger.Fatalf("Reverse-connect TLS configuration is incomplete (missing: %s). "+
			"Run 'tachyonikproxy reset-enrollment' and re-enroll.", strings.Join(missing, ", "))
	}

	for _, f := range fields {
		requireTLSFileReadable(f.name, cfg.AbsPath(f.path))
	}
}

// requireTLSFileReadable checks a TLS file the proxy needs and, on failure,
// says which of the two very different problems it is.
//
// "reset-enrollment and re-enroll" is right for a missing file and actively
// wrong for a permission error: re-enrolling as root was what created the
// unreadable material in the first place. Enrollment run under sudo used to
// leave <config-dir>/certs as root:root 0700 while the service runs as its own
// account, so the files inside came back EACCES even when `ls` showed them
// world-readable. Opening the file (not just Stat) is what distinguishes them —
// Stat succeeds on a file in a directory the process can traverse but whose
// contents it may not read.
func requireTLSFileReadable(name, path string) {
	f, err := os.Open(path)
	if err == nil {
		f.Close()
		return
	}
	logger.Fatalf("%s", tlsFileErrorMessage(name, path, err))
}

// tlsFileErrorMessage builds the operator-facing text for an unusable TLS file.
// Split out from requireTLSFileReadable because that one ends the process, and
// the branch that matters here — permission versus everything else — is exactly
// the part worth asserting on.
func tlsFileErrorMessage(name, path string, err error) string {
	if !os.IsPermission(err) {
		return fmt.Sprintf("TLS file %s=%q is not readable: %v. "+
			"Run 'tachyonikproxy reset-enrollment' and re-enroll.", name, path, err)
	}

	account := "this process's user"
	if u, uerr := user.Current(); uerr == nil {
		account = u.Username
	}
	// filepath.Dir twice: from <config-dir>/certs/client.crt back to the config
	// directory, which is the tree whose ownership is actually wrong.
	return fmt.Sprintf("TLS file %s=%q cannot be read by %s: %v.\n"+
		"The enrollment material exists but belongs to another account — "+
		"most likely it was written by 'sudo tachyonikproxy enroll' before the "+
		"ownership fix. Repair it with:\n"+
		"    sudo chown -R %s: %s",
		name, path, account, err, account, filepath.Dir(filepath.Dir(path)))
}

func setupLogging(cfg *config.Config) *os.File {
	var writers []io.Writer
	var logFile *os.File

	if cfg.Log.ToConsole {
		writers = append(writers, os.Stdout)
	}

	if cfg.Log.ToFile && cfg.Log.FilePath != "" {
		// Relative log paths resolve against the config file's directory,
		// not the CWD — `tachyonikproxy` must behave the same from any
		// working directory.
		logPath := cfg.AbsPath(cfg.Log.FilePath)
		_ = os.MkdirAll(filepath.Dir(logPath), 0750) // best effort; OpenFile reports real failures
		var err error
		logFile, err = os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err == nil {
			writers = append(writers, logFile)
		} else {
			fmt.Fprintf(os.Stderr, "Warning: cannot open log file %s: %v\n", logPath, err)
		}
	}

	if len(writers) == 0 {
		writers = append(writers, os.Stdout)
	}

	level := logger.ParseLogLevel(strings.ToUpper(cfg.Log.Level))
	logger.Setup(level, writers...)

	return logFile
}

func runEnroll() {
	opts := enroll.Options{
		ConfigPath: config.GetConfigPath(),
	}
	var listenMode bool
	var force bool
	var extraSANs []string

	// Collect all args except the program name and the "enroll" subcommand itself.
	var args []string
	for _, a := range os.Args[1:] {
		if a != "enroll" {
			args = append(args, a)
		}
	}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--config":
			i++
			if i < len(args) {
				opts.ConfigPath = args[i]
			}
		case "--cert-dir":
			i++
			if i < len(args) {
				opts.CertDir = args[i]
			}
		case "--port":
			i++
			if i < len(args) {
				p, err := fmt.Sscanf(args[i], "%d", &opts.Port)
				if err != nil || p == 0 {
					fmt.Fprintf(os.Stderr, "Invalid port: %s\n", args[i])
					os.Exit(1)
				}
			}
		case "--insecure":
			opts.Insecure = true
		case "--listen":
			listenMode = true
		case "--force":
			force = true
		case "--san":
			i++
			if i < len(args) {
				for _, s := range strings.Split(args[i], ",") {
					s = strings.TrimSpace(s)
					if s != "" {
						extraSANs = append(extraSANs, s)
					}
				}
			}
		default:
			// Last non-flag argument is the enrollment URL
			opts.EnrollmentURL = args[i]
		}
	}

	if listenMode {
		if opts.EnrollmentURL != "" {
			fmt.Fprintln(os.Stderr, "Error: --listen cannot be combined with an enrollment URL.")
			os.Exit(1)
		}
		listenOpts := enroll.ListenOptions{
			ConfigPath: opts.ConfigPath,
			CertDir:    opts.CertDir,
			Port:       opts.Port,
			ExtraSANs:  extraSANs,
			Force:      force,
		}
		if err := enroll.RunListen(listenOpts); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		return
	}

	if opts.EnrollmentURL == "" {
		fmt.Fprintln(os.Stderr, "Usage:")
		fmt.Fprintln(os.Stderr, "  tachyonikproxy [flags] enroll [flags] <enrollment-url>")
		fmt.Fprintln(os.Stderr, "  tachyonikproxy [flags] enroll --listen [flags]")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Flags:")
		fmt.Fprintln(os.Stderr, "  --config <path>    Path to config.yaml (default: auto-detect)")
		fmt.Fprintln(os.Stderr, "  --cert-dir <path>  Certificate output directory")
		fmt.Fprintln(os.Stderr, "  --port <port>      Override listening port (default: from config or 9100)")
		fmt.Fprintln(os.Stderr, "  --insecure         Skip TLS verification (online enroll; for self-signed setups)")
		fmt.Fprintln(os.Stderr, "  --listen           Reverse enrollment: wait for Tachyonik to dial in")
		fmt.Fprintln(os.Stderr, "  --san <names>      Comma-separated extra DNS names/IPs for the bootstrap cert (listen mode)")
		fmt.Fprintln(os.Stderr, "  --force            Skip the 'already enrolled' confirmation prompt")
		os.Exit(1)
	}

	if err := enroll.Run(opts); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func runResetEnrollment() {
	var force bool
	configPath := config.GetConfigPath()

	for _, a := range os.Args[1:] {
		if a == "reset-enrollment" {
			continue
		}
		if a == "--force" {
			force = true
			continue
		}
		if strings.HasPrefix(a, "--config=") {
			configPath = strings.TrimPrefix(a, "--config=")
		}
	}
	// Also accept "--config <path>" form.
	args := os.Args[1:]
	for i := 0; i < len(args); i++ {
		if args[i] == "--config" && i+1 < len(args) {
			configPath = args[i+1]
		}
	}

	// Load from the same path we will save back to — a --config override
	// must not clear one file based on another file's content.
	cfg := config.LoadFrom(configPath)
	certDir := ""
	// Derive cert dir from any configured cert path so we can clean up stray
	// files. Relative paths resolve against the config file's directory.
	for _, p := range []string{cfg.TLS.CACert, cfg.TLS.ServerCert, cfg.TLS.ServerKey, cfg.ReverseConnect.ClientCert, cfg.ReverseConnect.ClientKey} {
		if p != "" {
			certDir = filepath.Dir(cfg.AbsPath(p))
			break
		}
	}

	fmt.Println("This will delete the proxy's enrollment material:")
	if cfg.TLS.CACert != "" {
		fmt.Printf("  %s\n", cfg.TLS.CACert)
	}
	if cfg.TLS.ServerCert != "" {
		fmt.Printf("  %s\n", cfg.TLS.ServerCert)
	}
	if cfg.TLS.ServerKey != "" {
		fmt.Printf("  %s\n", cfg.TLS.ServerKey)
	}
	if cfg.ReverseConnect.ClientCert != "" {
		fmt.Printf("  %s\n", cfg.ReverseConnect.ClientCert)
	}
	if cfg.ReverseConnect.ClientKey != "" {
		fmt.Printf("  %s\n", cfg.ReverseConnect.ClientKey)
	}
	fmt.Println("  ... and clears tls.*, reverse_connect.*, connection_mode, proxy.name in")
	fmt.Printf("  %s\n", configPath)
	fmt.Println()

	if !force {
		fmt.Print("Type DELETE to confirm: ")
		var answer string
		fmt.Scanln(&answer)
		if answer != "DELETE" {
			fmt.Fprintln(os.Stderr, "Cancelled.")
			os.Exit(1)
		}
	}

	// Remove on-disk cert material (relative paths resolve against the
	// config file's directory).
	for _, p := range []string{cfg.TLS.CACert, cfg.TLS.ServerCert, cfg.TLS.ServerKey, cfg.ReverseConnect.ClientCert, cfg.ReverseConnect.ClientKey} {
		if p == "" {
			continue
		}
		if err := os.Remove(cfg.AbsPath(p)); err != nil && !os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "Warning: failed to remove %s: %v\n", cfg.AbsPath(p), err)
		}
	}
	// Also sweep any stale .tmp files from a crashed listen-mode persist.
	if certDir != "" {
		for _, base := range []string{"ca.crt.tmp", "server.crt.tmp", "server.key.tmp", "client.crt.tmp", "client.key.tmp"} {
			os.Remove(filepath.Join(certDir, base))
		}
	}

	// Clear the relevant config fields, preserve tools/log/server settings.
	cfg.TLS = config.TLSConfig{}
	cfg.ReverseConnect = config.ReverseConnectConfig{}
	cfg.ConnectionMode = ""
	cfg.Proxy.Name = ""
	cfg.AllowRemoteConfig = false

	if err := config.SaveConfig(cfg, configPath); err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to save config: %v\n", err)
		os.Exit(1)
	}

	fmt.Println()
	fmt.Println("Enrollment cleared. Re-enroll with one of:")
	fmt.Println("  tachyonikproxy enroll <enrollment-url>")
	fmt.Println("  tachyonikproxy enroll --listen")
}

// startNetScan builds the local-network scanner from config and starts its
// sweep loop. Returns nil when disabled or when the configured range is not one
// the proxy may sweep — in both cases the netscan JS API stays available but
// reports itself not ready, so detection routines degrade to "not detected"
// instead of failing.
//
// The return type is the interface, not *netscan.Scanner, deliberately: a nil
// *Scanner assigned into an interface makes that interface non-nil, so the
// disabled path would slip past the consumer's nil check and dereference.
func startNetScan(cfg *config.Config) toolscan.NetScanProvider {
	if !cfg.NetScan.Enabled {
		logger.Info("netscan: disabled by configuration")
		return nil
	}

	scanner, err := netscan.New(netscan.Config{
		Enabled:                cfg.NetScan.Enabled,
		IntervalMinutes:        cfg.NetScan.IntervalMinutes,
		Network:                cfg.NetScan.Network,
		Ports:                  cfg.NetScan.Ports,
		Concurrency:            cfg.NetScan.Concurrency,
		TimeoutSeconds:         cfg.NetScan.TimeoutSeconds,
		MaxBodyBytes:           cfg.NetScan.MaxBodyBytes,
		MaxScanDurationMinutes: cfg.NetScan.MaxScanDurationMinutes,
	})
	if err != nil {
		logger.Warnf("netscan: disabled — %v", err)
		return nil
	}

	go scanner.Run(context.Background())
	return scanner
}

// runNetScan performs a one-shot local-network sweep and prints what answered.
//
// It deliberately does not talk to a running daemon: the daemon's snapshot
// lives in its own memory and is never written to disk, so there is nothing for
// a second process to read. This sweeps afresh using the same configuration,
// which also means it works with no daemon running at all — useful for checking
// a network before deployment.
//
// netscan.enabled is ignored here. Disabling the periodic sweep is a statement
// about background behaviour; running this command is an explicit request.
func runNetScan() {
	jsonOutput := false
	for _, a := range os.Args[1:] {
		if a == "--json" {
			jsonOutput = true
		}
	}

	cfg := config.Load()
	scanner, err := netscan.New(netscan.Config{
		IntervalMinutes:        cfg.NetScan.IntervalMinutes,
		Network:                cfg.NetScan.Network,
		Ports:                  cfg.NetScan.Ports,
		Concurrency:            cfg.NetScan.Concurrency,
		TimeoutSeconds:         cfg.NetScan.TimeoutSeconds,
		MaxBodyBytes:           cfg.NetScan.MaxBodyBytes,
		MaxScanDurationMinutes: cfg.NetScan.MaxScanDurationMinutes,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "netscan: %v\n", err)
		os.Exit(1)
	}

	// Progress goes to stderr so stdout stays pipeable.
	fmt.Fprintf(os.Stderr, "Sweeping %s on port(s) %v ...\n", scanner.Network(), cfg.NetScan.Ports)

	scanner.ScanOnce(context.Background())
	snap := scanner.Snapshot()

	hosts := append([]netscan.Host(nil), snap.Hosts...)
	sort.Slice(hosts, func(i, j int) bool {
		a, b := net.ParseIP(hosts[i].IP).To4(), net.ParseIP(hosts[j].IP).To4()
		if a != nil && b != nil {
			ai, bi := binary.BigEndian.Uint32(a), binary.BigEndian.Uint32(b)
			if ai != bi {
				return ai < bi
			}
		}
		return hosts[i].Port < hosts[j].Port
	})

	if jsonOutput {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(map[string]interface{}{
			"network":    snap.Network,
			"lastScan":   snap.LastScan.Format(time.RFC3339),
			"durationMs": snap.DurationMS,
			"hosts":      hosts,
		})
		return
	}

	fmt.Printf("Network %s · %d responded · %s\n\n",
		snap.Network, len(hosts), time.Duration(snap.DurationMS)*time.Millisecond)

	if len(hosts) == 0 {
		fmt.Println("Nothing answered. If a service is expected, check the port list and that it speaks HTTPS.")
		return
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "HOST\tPORT\tSTATUS\tTLS\tSERVER\tTITLE")
	for _, h := range hosts {
		trust := "untrusted"
		if h.TLSTrusted {
			trust = "trusted"
		}
		fmt.Fprintf(w, "%s\t%d\t%d\t%s\t%s\t%s\n",
			h.IP, h.Port, h.Status, trust, truncate(h.Headers["server"], 24), truncate(h.Title(), 48))
	}
	w.Flush()
}

// truncate shortens s for column display, marking that it was cut.
func truncate(s string, max int) string {
	if s == "" {
		return "-"
	}
	if len(s) <= max {
		return s
	}
	return s[:max-1] + "…"
}

func runScan() {
	// Parse flags
	jsonOutput := false
	for _, a := range os.Args[1:] {
		if a == "scan" {
			continue
		}
		if a == "--json" {
			jsonOutput = true
		}
	}

	scanner := toolscan.New()
	results := scanner.Scan(nil)

	if jsonOutput {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(results)
		return
	}

	// Human-readable output
	var detected, notDetected []toolscan.ToolResult
	for _, r := range results {
		if r.Error != "" {
			notDetected = append(notDetected, r)
		} else if r.Detected {
			detected = append(detected, r)
		} else {
			notDetected = append(notDetected, r)
		}
	}

	fmt.Printf("Tachyonik TachyonikProxy — Tool Scan (%d rules)\n\n", len(results))

	if len(detected) > 0 {
		fmt.Println("Detected:")
		for _, r := range detected {
			ver := r.Version
			if ver == "" {
				ver = "?"
			}
			path := ""
			if r.Path != "" {
				path = "  (" + r.Path + ")"
			}
			fmt.Printf("  ✓ %-15s  %s%s\n", r.Name, ver, path)
		}
	}

	if len(notDetected) > 0 {
		if len(detected) > 0 {
			fmt.Println()
		}
		fmt.Println("Not detected:")
		for _, r := range notDetected {
			detail := ""
			if r.Error != "" {
				detail = "  [" + r.Error + "]"
			}
			fmt.Printf("  ✗ %-15s  %s%s\n", r.Name, r.Description, detail)
		}
	}

	fmt.Printf("\nTotal: %d detected, %d not detected\n", len(detected), len(notDetected))
}

func runHelp() {
	fmt.Printf("Tachyonik TachyonikProxy %s\n", version)
	fmt.Println()
	fmt.Println("Usage: tachyonikproxy [command]")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  (none)             Start the TachyonikProxy server (default)")
	fmt.Println("  enroll             Enroll this proxy (online: supply <url>; or --listen for reverse enrollment)")
	fmt.Println("  reset-enrollment   Clear all enrollment material and reset TLS config")
	fmt.Println("  scan               Scan for available tools on this host")
	fmt.Println("  netscan            Sweep the local network over HTTPS and list what answered")
	fmt.Println("                       --json                    full records including response bodies")
	fmt.Println("  self-update        Check (and optionally apply) an auto-update")
	fmt.Println("                       --dry-run                  check only, no changes")
	fmt.Println("                       --status                   print the local update history")
	fmt.Println("                       --bootstrap-layout         migrate this install to the versioned-symlink layout")
	fmt.Println("                       --reset-rollback-record    clear the sticky list of rolled-back versions")
	fmt.Println("  version            Print version and exit")
	fmt.Println("  help               Show this help message")
	printPlatformHelp()
}

// runSelfUpdate handles the `self-update` subcommand.
//
// Modes:
//
//	--dry-run                — check only, no changes (Phase 1 behaviour)
//	--bootstrap-layout       — one-time migration to the versioned-symlink layout
//	--reset-rollback-record  — clear the sticky rolled-back list
//	--status                 — print the local update-state.json summary
//	(no flag)                — check + apply + rollback if unhealthy (Phase 2)
func runSelfUpdate() {
	var dryRun, bootstrap, resetRollback, status bool
	for _, a := range os.Args[1:] {
		switch a {
		case "self-update":
		case "--dry-run":
			dryRun = true
		case "--bootstrap-layout":
			bootstrap = true
		case "--reset-rollback-record":
			resetRollback = true
		case "--status":
			status = true
		}
	}

	// A .deb / .rpm install belongs to dpkg or rpm, and the updater cannot
	// serve it: the packages put the binary at /usr/bin, both units execute
	// that path, and apply only ever rewrites /opt/tachyonik/proxy. Before the
	// marker existed this path "succeeded" on every timer tick while the
	// service kept running the old binary.
	//
	// --status stays available: reading local state tells an operator what
	// happened on this machine and changes nothing.
	if managed, msg := selfupdate.PackageManaged(); managed && !status {
		fmt.Println(msg)
		// Exit 0, not an error: this is an expected state, and a failing exit
		// would leave systemd marking the unit failed (and mailing the admin)
		// on any machine where a timer from an older package survives.
		os.Exit(0)
	}

	cfg := config.Load()

	paths, pathsErr := selfupdate.ResolveInstallPaths()

	// Every state-mutating subcommand acquires the cross-process lock —
	// not just the apply path. bootstrap and reset-rollback both write
	// to update-state.json or to the layout itself; without serialisation
	// against the timer-driven apply path they could corrupt state.
	// --status is read-only; the atomic-rename pattern on update-state.json
	// already protects readers.
	if bootstrap {
		if pathsErr != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", pathsErr)
			os.Exit(2)
		}
		lk, ok := acquireSelfUpdateLock(paths)
		if !ok {
			return
		}
		defer lk.Close()
		runBootstrap(paths)
		return
	}

	if resetRollback {
		if pathsErr != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", pathsErr)
			os.Exit(2)
		}
		lk, ok := acquireSelfUpdateLock(paths)
		if !ok {
			return
		}
		defer lk.Close()
		runResetRollback(paths)
		return
	}

	if status {
		if pathsErr != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", pathsErr)
			os.Exit(2)
		}
		runStatus(paths)
		return
	}

	if !cfg.AutoUpdate.Enabled {
		fmt.Println("Auto-update is disabled in config (auto_update.enabled: false). Nothing to do.")
		return
	}

	// Apply / dry-run path. Dry-run still skips the lock since it makes
	// no on-disk changes and racing two of them is harmless.
	if pathsErr == nil && !dryRun {
		lk, ok := acquireSelfUpdateLock(paths)
		if !ok {
			return
		}
		defer lk.Close()

		// Prune obsolete version directories opportunistically. Bounded
		// disk usage matters even when no update is available — an install
		// that hasn't seen an update in a while still benefits from
		// reclaiming space from past versions.
		stForPrune, _ := selfupdate.LoadState(paths.StateFile)
		if pruneRes, err := selfupdate.PruneOldVersions(paths, stForPrune, cfg.AutoUpdate.KeepVersions); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: prune failed: %v\n", err)
		} else if len(pruneRes.Removed) > 0 || len(pruneRes.StagingRemoved) > 0 {
			fmt.Printf("Pruned %d obsolete version dir(s)", len(pruneRes.Removed))
			if len(pruneRes.StagingRemoved) > 0 {
				fmt.Printf(" and %d stale staging dir(s)", len(pruneRes.StagingRemoved))
			}
			fmt.Printf(": %v\n", append(pruneRes.Removed, pruneRes.StagingRemoved...))
		}
	}

	pubKeys, err := selfupdate.LoadEmbeddedPubKeys()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to load embedded public keys: %v\n", err)
		os.Exit(2)
	}

	// Read state for replay protection (publishedAt) when available.
	var lastApplied time.Time
	var st *selfupdate.State
	if pathsErr == nil {
		st, _ = selfupdate.LoadState(paths.StateFile)
		if st != nil {
			lastApplied = st.LastAppliedPublishedAt
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	decision, err := selfupdate.Check(ctx, selfupdate.CheckOptions{
		ManifestURL:            cfg.AutoUpdate.ManifestURL,
		Channel:                cfg.AutoUpdate.Channel,
		CurrentVersion:         version,
		LastAppliedPublishedAt: lastApplied,
		PubKeys:                pubKeys,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(2)
	}

	printCheckHeader(cfg, len(pubKeys))
	if decision.Manifest != nil {
		fmt.Printf("  Manifest version: %s\n", decision.Manifest.LatestVersion)
		fmt.Printf("  Manifest channel: %s\n", decision.Manifest.Channel)
		fmt.Printf("  Manifest pub at:  %s\n", decision.Manifest.PublishedAt.Format(time.RFC3339))
	}
	fmt.Println()

	if !decision.UpdateAvailable {
		fmt.Printf("Result: NO UPDATE\n")
		fmt.Printf("  %s\n", decision.Reason)
		return
	}

	fmt.Printf("Result: UPDATE AVAILABLE\n")
	fmt.Printf("  %s\n", decision.Reason)
	if decision.Artifact != nil {
		fmt.Printf("  Artifact URL:    %s\n", decision.Artifact.URL)
		fmt.Printf("  Artifact sha256: %s\n", decision.Artifact.SHA256)
	}
	fmt.Println()

	if dryRun {
		fmt.Println("Note: --dry-run — no changes were made.")
		return
	}

	// Apply path.
	if pathsErr != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", pathsErr)
		os.Exit(2)
	}
	if !paths.LayoutBootstrapped() {
		fmt.Fprintln(os.Stderr, "Error: this install is not yet on the versioned-symlink layout.")
		fmt.Fprintln(os.Stderr, "       Run once as root: tachyonikproxy self-update --bootstrap-layout")
		os.Exit(2)
	}

	// Skip apply on an unenrolled proxy. Without TLS material the new
	// binary will refuse to start (per requireOutboundTLS), the health
	// probe will time out, and the version will end up on the sticky
	// rolled-back list — so when the operator finally enrolls and re-runs
	// self-update, we'd refuse to install that version. Better to no-op
	// now and try again on the next tick after enrollment.
	if cfg.ConnectionMode != "inbound" && (cfg.TLS.CACert == "" || cfg.TLS.ServerCert == "" || cfg.TLS.ServerKey == "") {
		fmt.Println("Skipping apply: proxy is not enrolled (TLS material not configured).")
		fmt.Println("Run 'tachyonikproxy enroll <enrollment-url>' or 'tachyonikproxy enroll --listen' first.")
		return
	}
	if cfg.ConnectionMode == "inbound" && (cfg.TLS.CACert == "" || cfg.ReverseConnect.ClientCert == "" || cfg.ReverseConnect.ClientKey == "") {
		fmt.Println("Skipping apply: proxy is not enrolled (reverse-connect TLS material not configured).")
		fmt.Println("Run 'tachyonikproxy enroll --listen' first.")
		return
	}

	runApply(ctx, cfg, paths, st, decision)
}

// acquireSelfUpdateLock takes the cross-process flock that gates every
// state-mutating self-update operation. Returns (lock, true) on success,
// (nil, false) when another invocation already holds the lock (in which
// case the caller should print a friendly message and return). Any other
// error exits the process — those are operational, not contention.
func acquireSelfUpdateLock(paths *selfupdate.InstallPaths) (*selfupdate.Lock, bool) {
	lk, err := selfupdate.AcquireLock(paths.StateFile)
	if err == nil {
		return lk, true
	}
	if errors.Is(err, selfupdate.ErrLockBusy) {
		fmt.Println("Another self-update invocation is already running; exiting.")
		return nil, false
	}
	fmt.Fprintf(os.Stderr, "Error: %v\n", err)
	os.Exit(2)
	return nil, false // unreachable
}

// logAutoUpdateStatus logs a single INFO line summarizing the most recent
// auto-update activity at daemon startup. The state file is the source of
// truth; failures to read it are logged at DEBUG and not treated as errors
// (a fresh install has no state yet).
func logAutoUpdateStatus() {
	paths, err := selfupdate.ResolveInstallPaths()
	if err != nil {
		logger.Debugf("Auto-update status: %v", err)
		return
	}
	st, err := selfupdate.LoadState(paths.StateFile)
	if err != nil {
		logger.Debugf("Auto-update status: load state: %v", err)
		return
	}
	if st.Active == "" && st.LastCheck.IsZero() {
		logger.Infof("Auto-update: no activity recorded yet")
		return
	}
	parts := []string{}
	if st.Active != "" {
		parts = append(parts, fmt.Sprintf("active=%s", st.Active))
	}
	if st.Previous != "" {
		parts = append(parts, fmt.Sprintf("previous=%s", st.Previous))
	}
	if !st.LastCheck.IsZero() {
		parts = append(parts, fmt.Sprintf("lastCheck=%s", st.LastCheck.Format(time.RFC3339)))
	}
	if st.Phase != selfupdate.PhaseIdle {
		parts = append(parts, fmt.Sprintf("phase=%s", st.Phase))
	}
	if len(st.RolledBack) > 0 {
		parts = append(parts, fmt.Sprintf("rolledBack=%v", st.RolledBack))
	}
	if st.LastError != "" {
		parts = append(parts, fmt.Sprintf("lastError=%q", st.LastError))
	}
	logger.Infof("Auto-update: %s", strings.Join(parts, " "))
}

func runStatus(paths *selfupdate.InstallPaths) {
	st, err := selfupdate.LoadState(paths.StateFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(2)
	}
	fmt.Printf("Tachyonik TachyonikProxy %s — auto-update status\n", version)
	fmt.Printf("  State file:    %s\n", paths.StateFile)
	fmt.Printf("  Version root:  %s\n", paths.VersionRoot)
	fmt.Printf("  Layout ready:  %t\n", paths.LayoutBootstrapped())
	fmt.Println()
	if cur, err := paths.CurrentVersionFromSymlink(); err == nil {
		fmt.Printf("  Active (symlink):     %s\n", cur)
	}
	if st.Active != "" {
		fmt.Printf("  Active (state file):  %s\n", st.Active)
	}
	if st.Previous != "" {
		fmt.Printf("  Previous:             %s\n", st.Previous)
	}
	fmt.Printf("  Phase:                %s\n", st.Phase)
	if !st.LastCheck.IsZero() {
		fmt.Printf("  Last check:           %s\n", st.LastCheck.Format(time.RFC3339))
	}
	if !st.LastAppliedPublishedAt.IsZero() {
		fmt.Printf("  Last applied (pub):   %s\n", st.LastAppliedPublishedAt.Format(time.RFC3339))
	}
	if st.LastError != "" {
		fmt.Printf("  Last error:           %s\n", st.LastError)
	}
	if st.InFlightVersion != "" {
		fmt.Printf("  In-flight version:    %s  (previous run did not complete cleanly)\n", st.InFlightVersion)
	}
	if len(st.RolledBack) > 0 {
		fmt.Printf("  Rolled-back (sticky): %v\n", st.RolledBack)
		fmt.Println("    These versions will not be auto-applied again. Use --reset-rollback-record to clear.")
	}
}

func printCheckHeader(cfg *config.Config, numKeys int) {
	fmt.Printf("Tachyonik TachyonikProxy %s — auto-update\n", version)
	fmt.Printf("  Manifest URL:     %s\n", cfg.AutoUpdate.ManifestURL)
	fmt.Printf("  Channel:          %s\n", cfg.AutoUpdate.Channel)
	fmt.Printf("  Trusted keys:     %d\n", numKeys)
}

func runBootstrap(paths *selfupdate.InstallPaths) {
	res, err := selfupdate.Bootstrap(paths, version)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Bootstrap failed: %v\n", err)
		os.Exit(2)
	}
	if res.AlreadyBootstrapped {
		fmt.Println("Layout is already bootstrapped — nothing to do.")
		fmt.Printf("  Version root:  %s\n", paths.VersionRoot)
		fmt.Printf("  Current link:  %s\n", paths.CurrentSymlink)
		fmt.Printf("  CLI symlink:   %s\n", paths.CLISymlink)
		return
	}
	fmt.Println("Layout bootstrapped successfully:")
	fmt.Printf("  Migrated from: %s\n", res.MovedFromPath)
	fmt.Printf("  Version dir:   %s\n", res.VersionDir)
	fmt.Printf("  Current link:  %s\n", res.CurrentSymlink)
	fmt.Printf("  CLI symlink:   %s\n", res.CLISymlink)
	fmt.Println()
	fmt.Println("If a system service was running the proxy, it can continue running")
	fmt.Println("on its existing inode; the next restart will pick up the new layout.")
}

func runResetRollback(paths *selfupdate.InstallPaths) {
	st, err := selfupdate.LoadState(paths.StateFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(2)
	}
	if len(st.RolledBack) == 0 {
		fmt.Println("No versions are on the rolled-back list. Nothing to do.")
		return
	}
	fmt.Println("Cleared the rolled-back list:")
	for _, v := range st.RolledBack {
		fmt.Printf("  - %s\n", v)
	}
	st.RolledBack = nil
	if err := selfupdate.SaveState(paths.StateFile, st); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to save state: %v\n", err)
		os.Exit(2)
	}
}

func runApply(ctx context.Context, cfg *config.Config, paths *selfupdate.InstallPaths, st *selfupdate.State, decision *selfupdate.Decision) {
	svc, err := selfupdate.ResolveServiceController(paths.Mode)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(2)
	}

	mode := selfupdate.HealthModeOutbound
	if cfg.ConnectionMode == "inbound" {
		mode = selfupdate.HealthModeInbound
	}
	healthOpts := selfupdate.HealthOptions{
		Mode:   mode,
		Host:   cfg.Server.Host,
		Port:   cfg.Server.Port,
		Window: time.Duration(cfg.AutoUpdate.HealthWindowSeconds) * time.Second,
	}

	if st == nil {
		st = &selfupdate.State{Phase: selfupdate.PhaseIdle}
	}

	fmt.Printf("Applying update %s → %s via %s ...\n",
		version, decision.Manifest.LatestVersion, svc.Name())

	res, err := selfupdate.Apply(ctx, st, selfupdate.ApplyOptions{
		Decision: decision,
		Paths:    paths,
		Service:  svc,
		Health:   healthOpts,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		if res != nil && res.RolledBack {
			fmt.Fprintf(os.Stderr, "Rolled back to %s.\n", res.PreviousVersion)
		}
		os.Exit(2)
	}

	if res.RolledBack {
		fmt.Printf("Update health check failed; rolled back to %s.\n", res.PreviousVersion)
		fmt.Printf("Version %s is recorded as rolled-back and will not be auto-applied again.\n", res.NewVersion)
		os.Exit(1)
	}

	fmt.Printf("Update applied successfully: now running %s (was %s).\n", res.NewVersion, res.PreviousVersion)
}
