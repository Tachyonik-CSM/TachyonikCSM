// TachyonikProxy
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

package enroll

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"tachyonik/tachyonikproxy/internal/config"
)

const (
	listenWindow          = 10 * time.Minute
	handshakeWindow       = 30 * time.Second
	perIPAttemptLimit     = 5
	perIPCooldown         = 60 * time.Second
	globalAttemptLimit    = 15
	ephemeralRSABits      = 4096
	bootstrapCertValidity = 15 * time.Minute
)

// ListenOptions configures RunListen.
type ListenOptions struct {
	ConfigPath string
	CertDir    string
	Port       int
	ExtraSANs  []string // additional DNS names or IPs to include in the bootstrap cert
	Force      bool     // proceed even if the proxy appears already enrolled
}

// helloRequest is POST /enroll/hello from ToolManager.
type helloRequest struct {
	PairingCode string `json:"pairingCode"`
}

// helloResponse carries the CSR plus reported identity back to ToolManager.
type helloResponse struct {
	CSRPEM           string   `json:"csrPEM"`
	ReportedHostname string   `json:"reportedHostname"`
	ReportedIPs      []string `json:"reportedIPs"`
	Port             int      `json:"port"`
}

// completeRequest is POST /enroll/complete from ToolManager after RM signs the CSR.
type completeRequest struct {
	ProxyName           string   `json:"proxyName"`
	CACert              string   `json:"caCert"`
	ServerCert          string   `json:"serverCert"`
	AllowedClients      []string `json:"allowedClients"`
	ToolManagerEndpoint string   `json:"toolManagerEndpoint,omitempty"`
}

type completeResponse struct {
	OK bool `json:"ok"`
}

type errorResponse struct {
	Error string `json:"error"`
}

type listenState int

const (
	stateListening listenState = iota
	stateHandshaking
	stateComplete
)

type listener struct {
	opts       ListenOptions
	cfg        *config.Config
	port       int
	certDir    string
	configPath string

	pairingCode     string // plaintext, for display only
	pairingCodeHash [32]byte

	ephemeralKey    *rsa.PrivateKey
	ephemeralKeyPEM string

	mu             sync.Mutex
	state          listenState
	handshakeUntil time.Time
	globalFails    int
	perIP          map[string]*ipAttempts

	// latched on first successful /enroll/hello — /enroll/complete requires the same
	handshakePeer string

	server *http.Server
	done   chan struct{}
	result error
}

type ipAttempts struct {
	fails         int
	cooldownUntil time.Time
}

// RunListen starts listen-mode reverse enrollment. Blocks until the enrollment
// completes, the window expires, or the attempt budget is exhausted.
func RunListen(opts ListenOptions) error {
	// Load from the path enrollment will write to — a --config override
	// must not be checked against a different auto-detected file.
	cfg := config.LoadFrom(opts.ConfigPath)

	if !opts.Force && cfg.TLS.CACert != "" && cfg.TLS.ServerCert != "" {
		fmt.Println("Warning: This proxy is already enrolled (TLS is configured).")
		fmt.Print("Re-enroll via listen mode? [y/N] ")
		var answer string
		fmt.Scanln(&answer)
		if !strings.HasPrefix(strings.ToLower(answer), "y") {
			return fmt.Errorf("enrollment cancelled")
		}
	}

	port := opts.Port
	if port == 0 {
		if p, err := parsePort(cfg.Server.Port); err == nil {
			port = p
		} else {
			port = 9100
		}
	}

	certDir := opts.CertDir
	if certDir == "" {
		certDir = filepath.Join(filepath.Dir(opts.ConfigPath), "certs")
	}

	l := &listener{
		opts:       opts,
		cfg:        cfg,
		port:       port,
		certDir:    certDir,
		configPath: opts.ConfigPath,
		state:      stateListening,
		perIP:      make(map[string]*ipAttempts),
		done:       make(chan struct{}),
	}

	if err := l.generateEphemeralMaterial(); err != nil {
		return err
	}

	bootstrapTLS, err := l.buildBootstrapTLSConfig()
	if err != nil {
		return fmt.Errorf("failed to build bootstrap TLS config: %w", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/enroll/hello", l.handleHello)
	mux.HandleFunc("/enroll/complete", l.handleComplete)

	addr := fmt.Sprintf(":%d", l.port)
	l.server = &http.Server{
		Addr:         addr,
		Handler:      mux,
		TLSConfig:    bootstrapTLS,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  30 * time.Second,
	}

	l.printBanner(addr)

	expireTimer := time.AfterFunc(listenWindow, func() {
		l.finish(fmt.Errorf("enrollment window expired (%s)", listenWindow))
	})
	defer expireTimer.Stop()

	go func() {
		if err := l.server.ListenAndServeTLS("", ""); err != nil && !errors.Is(err, http.ErrServerClosed) {
			l.finish(fmt.Errorf("listen-mode server failed: %w", err))
		}
	}()

	<-l.done
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = l.server.Shutdown(ctx)

	return l.result
}

func (l *listener) generateEphemeralMaterial() error {
	key, err := rsa.GenerateKey(rand.Reader, ephemeralRSABits)
	if err != nil {
		return fmt.Errorf("failed to generate ephemeral key: %w", err)
	}
	l.ephemeralKey = key
	l.ephemeralKeyPEM = string(pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	}))

	code, err := generatePairingCode()
	if err != nil {
		return fmt.Errorf("failed to generate pairing code: %w", err)
	}
	l.pairingCode = code
	// Store only the hash of the normalised (digits-only) code.
	l.pairingCodeHash = sha256.Sum256([]byte(stripCode(code)))
	return nil
}

// generatePairingCode returns a 6-digit numeric code formatted as XXX-XXX.
func generatePairingCode() (string, error) {
	max := big.NewInt(1000000)
	n, err := rand.Int(rand.Reader, max)
	if err != nil {
		return "", err
	}
	d := fmt.Sprintf("%06d", n.Int64())
	return d[:3] + "-" + d[3:], nil
}

func stripCode(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func (l *listener) buildBootstrapTLSConfig() (*tls.Config, error) {
	hostname, _ := os.Hostname()
	dnsNames := []string{"localhost"}
	if hostname != "" {
		dnsNames = append(dnsNames, hostname)
	}
	ips := []net.IP{net.ParseIP("127.0.0.1")}
	for _, s := range getLocalIPs() {
		if ip := net.ParseIP(s); ip != nil {
			ips = append(ips, ip)
		}
	}
	for _, s := range l.opts.ExtraSANs {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if ip := net.ParseIP(s); ip != nil {
			ips = append(ips, ip)
		} else {
			dnsNames = append(dnsNames, s)
		}
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, err
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "tachyonikproxy-listen"},
		DNSNames:     dnsNames,
		IPAddresses:  ips,
		NotBefore:    now,
		NotAfter:     now.Add(bootstrapCertValidity),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &l.ephemeralKey.PublicKey, l.ephemeralKey)
	if err != nil {
		return nil, err
	}
	cert := tls.Certificate{
		Certificate: [][]byte{der},
		PrivateKey:  l.ephemeralKey,
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS13,
	}, nil
}

func (l *listener) printBanner(addr string) {
	hostname, _ := os.Hostname()
	fmt.Println()
	fmt.Println("===========================================================")
	fmt.Println("  Tachyonik Proxy — Reverse Enrollment (listen mode)")
	fmt.Println("===========================================================")
	fmt.Println()
	fmt.Printf("  Listening on        %s (TLS 1.3, self-signed bootstrap cert)\n", addr)
	fmt.Printf("  Hostname            %s\n", hostname)
	ipList := getLocalIPs()
	if len(ipList) > 0 {
		fmt.Printf("  Local addresses     %s\n", strings.Join(ipList, ", "))
	}
	if len(l.opts.ExtraSANs) > 0 {
		fmt.Printf("  Additional SANs     %s\n", strings.Join(l.opts.ExtraSANs, ", "))
	}
	fmt.Printf("  Window              %s\n", listenWindow)
	fmt.Println()
	fmt.Printf("  Pairing code:       %s\n", l.pairingCode)
	fmt.Println()
	fmt.Println("  In the New Proxy dialog (setup selection: Reverse Enroll) enter")
	fmt.Println("  1. this pairing code into the \"Pairing code\" field.")
	fmt.Println("  2. one of the above listed local addresses as \"Proxy URL\".")
	fmt.Println()
	fmt.Println("  Do not share this code. It is single-use and expires")
	fmt.Println("  when this command exits.")
	fmt.Println("===========================================================")
	fmt.Println()
}

func (l *listener) finish(err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.state == stateComplete {
		return
	}
	l.state = stateComplete
	l.result = err
	close(l.done)
}

func (l *listener) clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// registerFailure bumps per-IP and global counters. Returns a non-nil error if
// the request must be refused (cool-down or global budget exhausted).
func (l *listener) registerFailure(ip string) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.globalFails++
	if l.globalFails >= globalAttemptLimit {
		go l.finish(fmt.Errorf("too many failed enrollment attempts (%d); listener terminated", l.globalFails))
		return fmt.Errorf("enrollment listener terminated after too many failed attempts")
	}

	a := l.perIP[ip]
	if a == nil {
		a = &ipAttempts{}
		l.perIP[ip] = a
	}
	a.fails++
	if a.fails >= perIPAttemptLimit {
		a.cooldownUntil = time.Now().Add(perIPCooldown)
		a.fails = 0
	}
	return nil
}

func (l *listener) checkCooldown(ip string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if a := l.perIP[ip]; a != nil && time.Now().Before(a.cooldownUntil) {
		return fmt.Errorf("too many attempts from this address; try again in %s", time.Until(a.cooldownUntil).Round(time.Second))
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func (l *listener) handleHello(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, errorResponse{Error: "method not allowed"})
		return
	}
	ip := l.clientIP(r)
	if err := l.checkCooldown(ip); err != nil {
		writeJSON(w, http.StatusTooManyRequests, errorResponse{Error: err.Error()})
		return
	}

	var req helloRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid request body"})
		return
	}

	submitted := sha256.Sum256([]byte(stripCode(req.PairingCode)))
	if subtle.ConstantTimeCompare(submitted[:], l.pairingCodeHash[:]) != 1 {
		if err := l.registerFailure(ip); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorResponse{Error: err.Error()})
			return
		}
		writeJSON(w, http.StatusUnauthorized, errorResponse{Error: "incorrect pairing code"})
		return
	}

	// Correct code. Latch handshake state; reject concurrent admins.
	l.mu.Lock()
	if l.state == stateComplete {
		l.mu.Unlock()
		writeJSON(w, http.StatusGone, errorResponse{Error: "enrollment already finished"})
		return
	}
	if l.state == stateHandshaking && time.Now().Before(l.handshakeUntil) && l.handshakePeer != ip {
		l.mu.Unlock()
		writeJSON(w, http.StatusConflict, errorResponse{Error: "another enrollment is in progress"})
		return
	}
	l.state = stateHandshaking
	l.handshakeUntil = time.Now().Add(handshakeWindow)
	l.handshakePeer = ip
	l.mu.Unlock()

	// Build a CSR from the ephemeral key. SANs are left empty — the signing side
	// applies the admin-supplied SANs. Reported hostname/IPs are informational.
	hostname, _ := os.Hostname()
	csrTemplate := x509.CertificateRequest{
		Subject: pkix.Name{CommonName: hostname},
	}
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, &csrTemplate, l.ephemeralKey)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to generate CSR"})
		return
	}
	csrPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER}))

	writeJSON(w, http.StatusOK, helloResponse{
		CSRPEM:           csrPEM,
		ReportedHostname: hostname,
		ReportedIPs:      getLocalIPs(),
		Port:             l.port,
	})
}

func (l *listener) handleComplete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, errorResponse{Error: "method not allowed"})
		return
	}
	ip := l.clientIP(r)

	l.mu.Lock()
	if l.state != stateHandshaking {
		l.mu.Unlock()
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "no handshake in progress"})
		return
	}
	if time.Now().After(l.handshakeUntil) {
		l.state = stateListening
		l.mu.Unlock()
		writeJSON(w, http.StatusRequestTimeout, errorResponse{Error: "handshake timed out"})
		return
	}
	if l.handshakePeer != ip {
		l.mu.Unlock()
		writeJSON(w, http.StatusForbidden, errorResponse{Error: "handshake peer mismatch"})
		return
	}
	l.mu.Unlock()

	var req completeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid request body"})
		return
	}
	if req.CACert == "" || req.ServerCert == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "caCert and serverCert are required"})
		return
	}
	// Verify the signed cert actually matches our ephemeral public key (catches a
	// bug or a malicious signer who swapped our CSR for someone else's key).
	if err := verifyCertMatchesKey(req.ServerCert, l.ephemeralKey); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: fmt.Sprintf("server cert does not match proxy key: %v", err)})
		return
	}

	resp := &Response{
		ProxyName:           req.ProxyName,
		ConnectionMode:      "outbound",
		ServerCert:          req.ServerCert,
		ServerKey:           l.ephemeralKeyPEM,
		CACert:              req.CACert,
		AllowedClients:      req.AllowedClients,
		ToolManagerEndpoint: req.ToolManagerEndpoint,
	}
	if _, err := PersistEnrollment(l.cfg, resp, l.certDir, l.configPath, ""); err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: err.Error()})
		// Don't finish — let the operator retry within the window. But bump the counter.
		_ = l.registerFailure(ip)
		return
	}

	writeJSON(w, http.StatusOK, completeResponse{OK: true})

	fmt.Println()
	fmt.Printf("Enrolled successfully as \"%s\" (mode: outbound)\n", req.ProxyName)
	fmt.Printf("  Certificates: %s\n", l.certDir)
	fmt.Printf("  Config:       %s\n", l.configPath)
	fmt.Println()
	printStartHint(l.configPath)

	go l.finish(nil)
}

// verifyCertMatchesKey parses certPEM and checks its public key matches key.
func verifyCertMatchesKey(certPEM string, key *rsa.PrivateKey) error {
	block, _ := pem.Decode([]byte(certPEM))
	if block == nil {
		return fmt.Errorf("not a PEM certificate")
	}
	c, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return err
	}
	pub, ok := c.PublicKey.(*rsa.PublicKey)
	if !ok {
		return fmt.Errorf("certificate public key is not RSA")
	}
	if pub.N.Cmp(key.PublicKey.N) != 0 || pub.E != key.PublicKey.E {
		return fmt.Errorf("public key mismatch")
	}
	return nil
}

func parsePort(s string) (int, error) {
	var p int
	_, err := fmt.Sscanf(s, "%d", &p)
	if err != nil || p <= 0 || p > 65535 {
		return 0, fmt.Errorf("invalid port %q", s)
	}
	return p, nil
}
