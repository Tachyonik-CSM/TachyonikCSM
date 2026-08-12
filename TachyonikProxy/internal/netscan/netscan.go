// TachyonikProxy
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package netscan periodically probes the proxy's own local network over HTTPS
// and keeps the responses in memory, so tool-detection routines can recognise a
// service by matching against already-collected data instead of shelling out to
// curl once per address.
//
// Separating the probing from the matching is the whole point. A routine that
// swept a /24 itself would need 254 sequential exec() calls inside a single
// detect() — far beyond the scanner's per-routine budget — and every routine
// would repeat the same sweep. Here one background sweep serves every routine,
// and a routine's own work reduces to a string match.
//
// The range is always private. Auto-derivation refuses anything outside
// RFC1918, and so does an explicitly configured CIDR: a proxy that happens to
// sit on a public address must never be talked into sweeping its neighbours.
package netscan

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"tachyonik/lib/logger"
)

// minPrefixLen bounds how large a configured network may be. /22 is 1022
// probeable addresses; anything wider is more plausibly a misconfiguration
// than an intent, and would keep a sweep running for many minutes.
const minPrefixLen = 22

// Config carries the netscan knobs. Zero values are replaced by defaults in
// New, so a caller may pass a partially filled struct.
type Config struct {
	Enabled                bool
	IntervalMinutes        int
	Network                string // explicit CIDR; empty means derive from the primary IPv4
	Ports                  []int
	Concurrency            int
	TimeoutSeconds         int
	MaxBodyBytes           int
	MaxScanDurationMinutes int
}

// Defaults applied to any unset field.
const (
	DefaultIntervalMinutes        = 60
	DefaultConcurrency            = 32
	DefaultTimeoutSeconds         = 3
	DefaultMaxBodyBytes           = 64 << 10 // 64 KiB
	DefaultMaxScanDurationMinutes = 10
)

// DefaultPorts is the shipped sweep. One port keeps a /24 at 254 probes;
// each additional port multiplies the work by the host count.
var DefaultPorts = []int{443}

// Host is the outcome of probing one address:port.
//
// Body is truncated at Config.MaxBodyBytes. TLSTrusted records whether the
// certificate would have validated against the system roots — the probe itself
// never requires it, since finding a service matters more than trusting it, but
// a routine may want to know.
//
// FinalURL is where the probe ended up after following redirects; it differs
// from URL when the service redirected. The certificate fields describe that
// final connection, and are often the only usable identifier when the body is
// not — an appliance behind an auth wall, or one whose root serves an empty
// shell that fills itself in via JavaScript, still presents a certificate
// naming the product.
type Host struct {
	IP           string            `json:"ip"`
	Port         int               `json:"port"`
	URL          string            `json:"url"`
	FinalURL     string            `json:"finalUrl"`
	Status       int               `json:"status"`
	Body         string            `json:"body"`
	Headers      map[string]string `json:"headers"`
	TLSTrusted   bool              `json:"tlsTrusted"`
	CertSubject  string            `json:"certSubject"`
	CertIssuer   string            `json:"certIssuer"`
	CertDNSNames []string          `json:"certDnsNames"`
	CertNotAfter string            `json:"certNotAfter"`
	Error        string            `json:"error"`
}

// Snapshot is an immutable view of the most recent completed sweep.
//
// Ready distinguishes "swept, found nothing" from "has not swept yet" — a
// routine must not report a tool absent merely because the proxy just started.
type Snapshot struct {
	Network    string    `json:"network"`
	Ready      bool      `json:"ready"`
	Scanning   bool      `json:"scanning"`
	LastScan   time.Time `json:"lastScan"`
	DurationMS int64     `json:"durationMs"`
	Hosts      []Host    `json:"hosts"`
}

// Scanner owns the sweep schedule and the cached results.
type Scanner struct {
	cfg     Config
	network *net.IPNet
	client  *http.Client
	roots   *x509.CertPool

	mu   sync.RWMutex
	snap Snapshot
}

// New validates cfg, resolves the network to sweep, and returns a Scanner.
// It performs no I/O beyond reading local interface addresses.
func New(cfg Config) (*Scanner, error) {
	applyDefaults(&cfg)

	ipNet, err := resolveNetwork(cfg.Network)
	if err != nil {
		return nil, err
	}

	// System roots are used only to answer "would this have been trusted?".
	// A failure to load them is not fatal: probing continues, and TLSTrusted
	// is simply reported false throughout.
	roots, _ := x509.SystemCertPool()

	return &Scanner{
		cfg:     cfg,
		network: ipNet,
		roots:   roots,
		client: &http.Client{
			Timeout:       time.Duration(cfg.TimeoutSeconds) * time.Second,
			CheckRedirect: checkRedirect,
			Transport: &http.Transport{
				// Deliberate: the point is to find services, including the
				// self-signed and expired ones that a LAN appliance ships
				// with. Trust is reported per host, never required.
				TLSClientConfig:     &tls.Config{InsecureSkipVerify: true},
				DisableKeepAlives:   true,
				MaxIdleConnsPerHost: -1,
			},
		},
		snap: Snapshot{Network: ipNet.String()},
	}, nil
}

func applyDefaults(cfg *Config) {
	if cfg.IntervalMinutes <= 0 {
		cfg.IntervalMinutes = DefaultIntervalMinutes
	}
	if cfg.Concurrency <= 0 {
		cfg.Concurrency = DefaultConcurrency
	}
	if cfg.TimeoutSeconds <= 0 {
		cfg.TimeoutSeconds = DefaultTimeoutSeconds
	}
	if cfg.MaxBodyBytes <= 0 {
		cfg.MaxBodyBytes = DefaultMaxBodyBytes
	}
	if cfg.MaxScanDurationMinutes <= 0 {
		cfg.MaxScanDurationMinutes = DefaultMaxScanDurationMinutes
	}
	if len(cfg.Ports) == 0 {
		cfg.Ports = append([]int(nil), DefaultPorts...)
	}
}

// resolveNetwork returns the network to sweep: the given CIDR, or the /24
// around the primary IPv4 when cidr is empty. Either way the result must be
// private and no larger than minPrefixLen.
func resolveNetwork(cidr string) (*net.IPNet, error) {
	if strings.TrimSpace(cidr) == "" {
		ip := PrimaryIPv4()
		if ip == "" {
			return nil, fmt.Errorf("no non-loopback IPv4 address found; set netscan.network explicitly")
		}
		cidr = ip + "/24"
	}

	_, ipNet, err := net.ParseCIDR(cidr)
	if err != nil {
		return nil, fmt.Errorf("invalid netscan.network %q: %w", cidr, err)
	}
	ip4 := ipNet.IP.To4()
	if ip4 == nil {
		return nil, fmt.Errorf("netscan.network %q is not IPv4; only IPv4 networks are swept", cidr)
	}
	if !isSweepableIPv4(ip4) {
		return nil, fmt.Errorf(
			"netscan.network %q is outside the sweepable ranges (10/8, 172.16/12, 192.168/16, 127/8); "+
				"refusing to sweep a public network", ipNet.String())
	}
	if ones, _ := ipNet.Mask.Size(); ones < minPrefixLen {
		return nil, fmt.Errorf("netscan.network %q is larger than /%d; narrow the range", ipNet.String(), minPrefixLen)
	}
	return ipNet, nil
}

// isSweepableIPv4 reports whether ip belongs to a range this proxy may probe:
// the RFC1918 private blocks, plus loopback. Everything else is somebody
// else's network — a proxy that happens to hold a public address must never be
// talked into sweeping its neighbours, whether by auto-derivation or by a
// mistyped netscan.network.
func isSweepableIPv4(ip net.IP) bool {
	ip4 := ip.To4()
	if ip4 == nil {
		return false
	}
	switch {
	case ip4[0] == 10:
		return true
	case ip4[0] == 172 && ip4[1] >= 16 && ip4[1] <= 31:
		return true
	case ip4[0] == 192 && ip4[1] == 168:
		return true
	case ip4[0] == 127: // loopback: our own machine, safe and useful for local services
		return true
	}
	return false
}

// PrimaryIPv4 returns the first non-loopback IPv4 address bound to an active
// interface, or "" when there is none. On a multi-homed host "primary" is
// operator-defined; a single deterministic pick is enough here, and an operator
// who disagrees sets netscan.network explicitly.
func PrimaryIPv4() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return ""
	}
	for _, a := range addrs {
		ipNet, ok := a.(*net.IPNet)
		if !ok || ipNet.IP.IsLoopback() {
			continue
		}
		if ip4 := ipNet.IP.To4(); ip4 != nil {
			return ip4.String()
		}
	}
	return ""
}

// Network returns the CIDR this scanner sweeps.
func (s *Scanner) Network() string { return s.network.String() }

// Snapshot returns the current cached view. Safe for concurrent use; the
// returned Hosts slice is not modified after publication.
//
// A nil receiver yields the zero Snapshot rather than panicking. That is the
// "netscan disabled" path: callers hold this behind an interface, where a nil
// *Scanner makes the interface itself non-nil, so a nil check at the call site
// does not protect them.
func (s *Scanner) Snapshot() Snapshot {
	if s == nil {
		return Snapshot{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.snap
}

// Run sweeps immediately, then on every interval tick, until ctx is cancelled.
// It is meant to be started in its own goroutine: nothing here touches the
// caller's path, and a sweep in progress never blocks Snapshot.
func (s *Scanner) Run(ctx context.Context) {
	logger.Infof("netscan: sweeping %s every %d minute(s), ports %v, concurrency %d",
		s.network.String(), s.cfg.IntervalMinutes, s.cfg.Ports, s.cfg.Concurrency)

	s.ScanOnce(ctx)

	t := time.NewTicker(time.Duration(s.cfg.IntervalMinutes) * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.ScanOnce(ctx)
		}
	}
}

// ScanOnce performs a single sweep and publishes the result. Exported so the
// daemon can trigger an out-of-band scan.
func (s *Scanner) ScanOnce(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, time.Duration(s.cfg.MaxScanDurationMinutes)*time.Minute)
	defer cancel()

	s.setScanning(true)
	defer s.setScanning(false)

	started := time.Now()
	targets := s.targets()
	hosts := s.probeAll(ctx, targets)
	elapsed := time.Since(started)

	s.mu.Lock()
	s.snap = Snapshot{
		Network:    s.network.String(),
		Ready:      true,
		Scanning:   true, // cleared by the deferred setScanning(false)
		LastScan:   started,
		DurationMS: elapsed.Milliseconds(),
		Hosts:      hosts,
	}
	s.mu.Unlock()

	logger.Infof("netscan: swept %d target(s) in %s, %d responded",
		len(targets), elapsed.Round(time.Millisecond), len(hosts))
}

func (s *Scanner) setScanning(v bool) {
	s.mu.Lock()
	s.snap.Scanning = v
	s.mu.Unlock()
}

type target struct {
	ip   string
	port int
}

// targets enumerates every usable address in the network, crossed with the
// configured ports. The network and broadcast addresses are skipped.
func (s *Scanner) targets() []target {
	baseIP := s.network.IP.Mask(s.network.Mask).To4()
	if baseIP == nil {
		return nil
	}

	ones, bits := s.network.Mask.Size()
	hostBits := uint(bits - ones)
	base := binary.BigEndian.Uint32(baseIP)
	total := uint32(1) << hostBits

	var out []target
	// Skip the network address and the broadcast address.
	for i := uint32(1); i+1 < total; i++ {
		var b [4]byte
		binary.BigEndian.PutUint32(b[:], base+i)
		ip := net.IP(b[:]).String()
		for _, p := range s.cfg.Ports {
			out = append(out, target{ip: ip, port: p})
		}
	}

	// A /31 or /32 has no usable range under that rule; probe the address
	// itself rather than nothing.
	if len(out) == 0 {
		for _, p := range s.cfg.Ports {
			out = append(out, target{ip: baseIP.String(), port: p})
		}
	}
	return out
}

// probeAll runs the targets through a bounded worker pool and returns only
// those that responded. Unreachable addresses — the overwhelming majority on a
// typical LAN — are dropped rather than cached, so a routine iterating hosts()
// sees only real services.
func (s *Scanner) probeAll(ctx context.Context, targets []target) []Host {
	if len(targets) == 0 {
		return nil
	}

	workers := s.cfg.Concurrency
	if workers > len(targets) {
		workers = len(targets)
	}

	in := make(chan target)
	results := make(chan Host)

	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			for t := range in {
				if h, ok := s.probe(ctx, t); ok {
					select {
					case results <- h:
					case <-ctx.Done():
						return
					}
				}
			}
		}()
	}

	go func() {
		defer close(in)
		for _, t := range targets {
			select {
			case in <- t:
			case <-ctx.Done():
				return
			}
		}
	}()

	go func() {
		wg.Wait()
		close(results)
	}()

	var hosts []Host
	for h := range results {
		hosts = append(hosts, h)
	}
	return hosts
}

// maxRedirects bounds how far a probe will chase a redirect chain.
const maxRedirects = 3

// checkRedirect follows a redirect only when it stays on the address we
// probed — any port, any path, and hostnames that resolve back to it.
//
// Not following at all meant an appliance that answers "/" with a 302 to its
// login page was cached as an empty body, since a redirect response has no
// content. Following anywhere would be worse: it would probe hosts the sweep
// never selected, and attribute another machine's page to this record. Same
// address keeps both the private-range guarantee and a coherent record, while
// covering what appliances actually do — "/" to "/login", or 443 to a product
// port such as 9392.
//
// A redirect that leaves the address is not followed; the 3xx and its Location
// header are recorded instead, which is still a usable detection signal.
func checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= maxRedirects {
		return http.ErrUseLastResponse
	}
	if len(via) == 0 {
		return nil
	}
	if !resolvesToSameHost(req.URL.Hostname(), via[0].URL.Hostname()) {
		return http.ErrUseLastResponse
	}
	return nil
}

// resolvesToSameHost reports whether target is, or resolves to, origin.
// origin is always an IP literal here (the swept address). Lookups are bounded
// so a slow resolver cannot stall a sweep.
func resolvesToSameHost(target, origin string) bool {
	if target == origin {
		return true
	}
	if net.ParseIP(target) != nil {
		return false // a different IP literal: not our address
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	addrs, err := net.DefaultResolver.LookupHost(ctx, target)
	if err != nil {
		return false
	}
	for _, a := range addrs {
		if a == origin {
			return true
		}
	}
	return false
}

// probe performs one HTTPS GET. The bool reports whether anything answered:
// a refused connection or timeout is the normal case for most of a subnet and
// is not worth caching.
func (s *Scanner) probe(ctx context.Context, t target) (Host, bool) {
	url := fmt.Sprintf("https://%s:%d/", t.ip, t.port)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return Host{}, false
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return Host{}, false
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, int64(s.cfg.MaxBodyBytes)))

	h := Host{
		IP:       t.ip,
		Port:     t.port,
		URL:      url,
		FinalURL: url,
		Status:   resp.StatusCode,
		Body:     string(body),
		Headers:  flattenHeaders(resp.Header),
	}
	// resp.Request is the last request made, so this reflects any redirects
	// that were followed.
	if resp.Request != nil && resp.Request.URL != nil {
		h.FinalURL = resp.Request.URL.String()
	}

	// The certificate belongs to the connection the response came from, which
	// after a followed redirect is the final one.
	if resp.TLS != nil && len(resp.TLS.PeerCertificates) > 0 {
		leaf := resp.TLS.PeerCertificates[0]
		h.CertSubject = leaf.Subject.String()
		h.CertIssuer = leaf.Issuer.String()
		h.CertDNSNames = leaf.DNSNames
		h.CertNotAfter = leaf.NotAfter.UTC().Format(time.RFC3339)
		h.TLSTrusted = s.verifyChain(leaf, resp.TLS.PeerCertificates[1:], t.ip)
	}

	return h, true
}

// verifyChain answers whether the presented certificate would have validated
// normally. Reported for information only — the probe never depends on it.
func (s *Scanner) verifyChain(leaf *x509.Certificate, intermediates []*x509.Certificate, host string) bool {
	if s.roots == nil {
		return false
	}
	inter := x509.NewCertPool()
	for _, c := range intermediates {
		inter.AddCert(c)
	}
	_, err := leaf.Verify(x509.VerifyOptions{
		DNSName:       host,
		Roots:         s.roots,
		Intermediates: inter,
	})
	return err == nil
}

// titleRe extracts the contents of a <title> element. Deliberately tolerant:
// attributes on the tag, mixed case, and newlines inside the text all occur in
// the wild, and this only feeds a diagnostic listing.
var titleRe = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)

// Title returns the response's <title> text with whitespace collapsed, or "".
// Together with the Server header it is what usefully reads as a "banner" for
// a host — the raw body rarely does.
func (h Host) Title() string {
	m := titleRe.FindStringSubmatch(h.Body)
	if len(m) < 2 {
		return ""
	}
	return strings.Join(strings.Fields(m[1]), " ")
}

func flattenHeaders(h http.Header) map[string]string {
	out := make(map[string]string, len(h))
	for k, v := range h {
		if len(v) > 0 {
			out[strings.ToLower(k)] = v[0]
		}
	}
	return out
}
