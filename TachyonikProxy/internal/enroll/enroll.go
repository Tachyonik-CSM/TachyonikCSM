// TachyonikProxy
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

package enroll

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"tachyonik/tachyonikproxy/internal/config"
)

// Request is sent to the enrollment endpoint.
type Request struct {
	Token       string   `json:"token"`
	Hostname    string   `json:"hostname"`
	Port        int      `json:"port"`
	IPAddresses []string `json:"ipAddresses"`
}

// Response is returned by the enrollment endpoint.
type Response struct {
	ProxyName           string   `json:"proxyName"`
	ConnectionMode      string   `json:"connectionMode"`
	ServerCert          string   `json:"serverCert,omitempty"`
	ServerKey           string   `json:"serverKey,omitempty"`
	CACert              string   `json:"caCert"`
	ClientCert          string   `json:"clientCert,omitempty"`
	ClientKey           string   `json:"clientKey,omitempty"`
	AllowedClients      []string `json:"allowedClients,omitempty"`
	ToolManagerEndpoint string   `json:"toolManagerEndpoint,omitempty"`
	URLUpdated          bool     `json:"urlUpdated"`
	URLWarning          string   `json:"urlWarning,omitempty"`
}

// Options configures the enrollment process.
type Options struct {
	EnrollmentURL string
	ConfigPath    string
	CertDir       string
	Port          int
	Insecure      bool
}

// Run executes the enrollment process.
func Run(opts Options) error {
	// Parse enrollment URL
	u, err := url.Parse(opts.EnrollmentURL)
	if err != nil {
		return fmt.Errorf("invalid enrollment URL: %w", err)
	}

	// Extract token from query parameter
	token := u.Query().Get("token")
	if token == "" {
		return fmt.Errorf("enrollment URL must contain a ?token= parameter")
	}

	// Build the POST URL (strip query params, ensure path ends with /api/proxy-enroll)
	postURL := fmt.Sprintf("%s://%s/api/proxy-enroll", u.Scheme, u.Host)

	// Check HTTPS requirement
	if u.Scheme != "https" && !opts.Insecure {
		return fmt.Errorf("enrollment URL must use HTTPS (use --insecure to override for development)")
	}

	// Detect hostname and IPs
	hostname, _ := os.Hostname()
	ipAddresses := getLocalIPs()

	port := opts.Port
	if port == 0 {
		port = 9100
	}

	// Check if already enrolled. Load from the path enrollment will write
	// to — a --config override must not be checked (and later merged)
	// against a different file picked up by auto-detection.
	cfg := config.LoadFrom(opts.ConfigPath)
	if cfg.TLS.CACert != "" && cfg.TLS.ServerCert != "" {
		fmt.Println("Warning: This proxy is already enrolled (TLS is configured).")
		fmt.Print("Re-enroll? [y/N] ")
		var answer string
		fmt.Scanln(&answer)
		if !strings.HasPrefix(strings.ToLower(answer), "y") {
			return fmt.Errorf("enrollment cancelled")
		}
	}

	// Build request
	reqBody := Request{
		Token:       token,
		Hostname:    hostname,
		Port:        port,
		IPAddresses: ipAddresses,
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("failed to marshal request: %w", err)
	}

	// Create HTTP client
	httpClient := &http.Client{Timeout: 30 * time.Second}
	if opts.Insecure {
		httpClient.Transport = &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		}
		fmt.Println("Warning: TLS certificate verification disabled (--insecure)")
	}

	fmt.Printf("Enrolling with Tachyonik at %s...\n", u.Host)

	// Send enrollment request
	resp, err := httpClient.Post(postURL, "application/json", bytes.NewReader(bodyBytes))
	if err != nil {
		if strings.Contains(err.Error(), "certificate") || strings.Contains(err.Error(), "x509") || strings.Contains(err.Error(), "tls") {
			fmt.Fprintln(os.Stderr, "")
			fmt.Fprintln(os.Stderr, "Hint: If you are using a self-signed certificate (e.g. for development),")
			fmt.Fprintln(os.Stderr, "      re-run with --insecure to skip TLS verification (not recommended for production):")
			fmt.Fprintf(os.Stderr, "      tachyonikproxy enroll --insecure \"%s\"\n", opts.EnrollmentURL)
		}
		return fmt.Errorf("failed to contact enrollment server: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("enrollment failed (HTTP %d): %s", resp.StatusCode, string(respBody))
	}

	var enrollResp Response
	if err := json.Unmarshal(respBody, &enrollResp); err != nil {
		return fmt.Errorf("failed to parse enrollment response: %w", err)
	}

	certDir := opts.CertDir
	if certDir == "" {
		certDir = filepath.Join(filepath.Dir(opts.ConfigPath), "certs")
	}

	pres, err := PersistEnrollment(cfg, &enrollResp, certDir, opts.ConfigPath, u.Hostname())
	if err != nil {
		return err
	}
	connMode := pres.ConnectionMode

	// Print success
	fmt.Println()
	fmt.Printf("Enrolled successfully as \"%s\" (mode: %s)\n", enrollResp.ProxyName, connMode)
	fmt.Println()
	fmt.Printf("  Certificates written to: %s\n", certDir)
	fmt.Printf("  Config updated:          %s\n", opts.ConfigPath)
	fmt.Println()

	if enrollResp.URLWarning != "" {
		fmt.Printf("  WARNING: %s\n", enrollResp.URLWarning)
		fmt.Println()
	}

	if enrollResp.URLUpdated {
		fmt.Printf("  Proxy URL set to: https://%s:%d\n", hostname, port)
		fmt.Println()
	}

	if connMode == "inbound" {
		fmt.Printf("  ToolManager endpoint: %s\n", cfg.ReverseConnect.ToolManagerURL)
		fmt.Println()
	}

	printStartHint(opts.ConfigPath)

	return nil
}

// printStartHint tells the operator how to actually start the proxy after
// enrollment — systemctl/launchctl for system installs, a direct command for
// user-space installs. Detection is based on where the config file lives:
// under the user's ~/.config/tachyonik/ (user-space install, per the installer)
// or under /etc/tachyonik/ or the platform's program data directory (system
// install). Other paths fall through to a generic interactive hint.
func printStartHint(configPath string) {
	abs, err := filepath.Abs(configPath)
	if err != nil {
		abs = configPath
	}
	norm := filepath.ToSlash(abs)
	home, _ := os.UserHomeDir()
	userInstall := home != "" && strings.HasPrefix(norm, filepath.ToSlash(home)+"/.config/tachyonik/")
	// Also recognise an XDG_CONFIG_HOME-based location — the binary's
	// auto-detect search covers it just the same.
	if up := config.UserConfigPath(); up != "" && norm == filepath.ToSlash(up) {
		userInstall = true
	}
	systemInstall := strings.HasPrefix(norm, "/etc/tachyonik/") ||
		strings.Contains(norm, "/Program Files/Tachyonik/") ||
		strings.Contains(norm, "/ProgramData/Tachyonik/")

	switch {
	case userInstall:
		// The canonical user-space config location is part of the binary's
		// auto-detect search order — no env var needed.
		fmt.Println("  Start the proxy (user-space install):")
		fmt.Println("    tachyonikproxy")
		fmt.Println()
		fmt.Println("  Note: user-space installs do not auto-start on boot. Add the command")
		fmt.Println("  to crontab (@reboot) or a user systemd unit if you need it persistent.")
	case systemInstall:
		fmt.Println("  Start the service:")
		switch runtime.GOOS {
		case "darwin":
			fmt.Println("    sudo launchctl load /Library/LaunchDaemons/com.tachyonik.tachyonikproxy.plist")
		case "windows":
			fmt.Println("    net start TachyonikTachyonikProxy")
		default:
			fmt.Println("    sudo systemctl start tachyonikproxy")
		}
		fmt.Println()
		fmt.Println("  Or run interactively:")
		fmt.Println("    tachyonikproxy")
	default:
		// A non-canonical location (e.g. CWD enrollment with --config):
		// auto-detect only finds ./config.yaml when running from that
		// directory, so spell out the env var for the general case.
		fmt.Println("  Start the proxy:")
		fmt.Printf("    TACHYONIKPROXY_CONFIG=\"%s\" tachyonikproxy\n", configPath)
	}
}

// PersistResult describes what PersistEnrollment wrote.
type PersistResult struct {
	ConnectionMode string
	CertDir        string
}

// PersistEnrollment writes the cert material from an enrollment response to disk
// and updates the config file. Shared by the online enroll flow (Run) and the
// listen-mode flow (RunListen). When resp.ServerKey is empty for outbound mode
// the existing key at <certDir>/server.key is left in place (the listen-mode
// caller writes it separately before calling).
func PersistEnrollment(cfg *config.Config, resp *Response, certDir, configPath, enrollHost string) (*PersistResult, error) {
	// 0700: only the proxy user (or root) should be able to read or even
	// list the cert directory. Key files inside are 0600, so the directory
	// is defence in depth — without it, other local users could enumerate
	// which cert files exist. We Chmod after MkdirAll so a directory left
	// over from a prior, looser-defaulted release is brought into line.
	if err := os.MkdirAll(certDir, 0700); err != nil {
		return nil, fmt.Errorf("failed to create cert directory %s: %w", certDir, err)
	}
	if err := os.Chmod(certDir, 0700); err != nil {
		return nil, fmt.Errorf("failed to chmod cert directory %s: %w", certDir, err)
	}

	// Paths stored in the config are made relative to the config file's
	// directory when the certs live underneath it (the normal layout:
	// <config-dir>/certs). The binary resolves them against the config
	// dir at use time, so the config+certs bundle works from any CWD and
	// can be moved as a unit. Paths outside the config dir (explicit
	// --cert-dir elsewhere) are stored absolute.
	configDir, err := filepath.Abs(filepath.Dir(configPath))
	if err != nil {
		configDir = filepath.Dir(configPath)
	}
	storePath := func(p string) string {
		abs, err := filepath.Abs(p)
		if err != nil {
			return p
		}
		rel, err := filepath.Rel(configDir, abs)
		if err != nil || strings.HasPrefix(rel, "..") {
			return abs
		}
		return rel
	}

	caCertPath := filepath.Join(certDir, "ca.crt")
	if err := writeFileAtomic(caCertPath, []byte(resp.CACert), 0644); err != nil {
		return nil, fmt.Errorf("failed to write CA cert: %w", err)
	}

	cfg.Proxy.Name = resp.ProxyName
	cfg.AllowRemoteConfig = true

	// Note on the log path: config.LoadFrom already pointed a fresh config
	// (no file on disk yet) at the canonical log location for this
	// privilege level; an existing config keeps the operator's choice.
	// SaveConfig below persists whichever applies.

	connMode := resp.ConnectionMode
	if connMode == "" {
		connMode = "outbound"
	}

	if connMode == "inbound" {
		clientCertPath := filepath.Join(certDir, "client.crt")
		clientKeyPath := filepath.Join(certDir, "client.key")

		if err := writeFileAtomic(clientCertPath, []byte(resp.ClientCert), 0644); err != nil {
			return nil, fmt.Errorf("failed to write client cert: %w", err)
		}
		if err := writeFileAtomic(clientKeyPath, []byte(resp.ClientKey), 0600); err != nil {
			return nil, fmt.Errorf("failed to write client key: %w", err)
		}

		tmEndpoint := resp.ToolManagerEndpoint
		if tmEndpoint == "" && enrollHost != "" {
			tmEndpoint = fmt.Sprintf("wss://%s:9101/ws/proxy", enrollHost)
		}

		cfg.ConnectionMode = "inbound"
		cfg.TLS = config.TLSConfig{CACert: storePath(caCertPath)}
		cfg.ReverseConnect = config.ReverseConnectConfig{
			ToolManagerURL: tmEndpoint,
			ClientCert:     storePath(clientCertPath),
			ClientKey:      storePath(clientKeyPath),
		}
	} else {
		serverCertPath := filepath.Join(certDir, "server.crt")
		serverKeyPath := filepath.Join(certDir, "server.key")

		if err := writeFileAtomic(serverCertPath, []byte(resp.ServerCert), 0644); err != nil {
			return nil, fmt.Errorf("failed to write server cert: %w", err)
		}
		if resp.ServerKey != "" {
			if err := writeFileAtomic(serverKeyPath, []byte(resp.ServerKey), 0600); err != nil {
				return nil, fmt.Errorf("failed to write server key: %w", err)
			}
		}

		cfg.ConnectionMode = "outbound"
		cfg.TLS = config.TLSConfig{
			CACert:         storePath(caCertPath),
			ServerCert:     storePath(serverCertPath),
			ServerKey:      storePath(serverKeyPath),
			AllowedClients: resp.AllowedClients,
		}
	}

	if err := config.SaveConfig(cfg, configPath); err != nil {
		return nil, fmt.Errorf("failed to save config: %w", err)
	}

	return &PersistResult{ConnectionMode: connMode, CertDir: certDir}, nil
}

// writeFileAtomic writes data to path via a .tmp sibling + rename, fsyncing the
// temp file first so a crash mid-write leaves the original file intact.
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// getLocalIPs returns a list of non-loopback IP addresses.
func getLocalIPs() []string {
	var ips []string
	ifaces, err := net.Interfaces()
	if err != nil {
		return ips
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		if iface.Flags&net.FlagUp == 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			var ip net.IP
			switch v := addr.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if ip != nil && !ip.IsLoopback() {
				ips = append(ips, ip.String())
			}
		}
	}
	return ips
}
