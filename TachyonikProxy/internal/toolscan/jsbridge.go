// TachyonikProxy
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

package toolscan

import (
	"crypto/tls"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/dop251/goja"

	"tachyonik/tachyonikproxy/internal/netscan"
)

// Bounds on the httpGet helper. A routine reaching outside the periodic sweep
// still must not be able to hang the scanner or exhaust its memory.
var (
	defaultHTTPTimeout  = 5 * time.Second
	maxHTTPTimeout      = 30 * time.Second
	defaultHTTPBodyCap  = int64(1 << 20) // 1 MiB
	defaultHTTPRedirect = false
)

// registerNetScan installs the `netscan` global: a read-only view of the most
// recent local-network sweep.
//
// Every call is a lookup over already-collected data, so a routine that used to
// need one exec("curl", ...) per address — 254 of them for a /24, far beyond
// the per-routine budget — becomes a single string match. The sweep itself runs
// on its own schedule; nothing here performs I/O.
func (s *Scanner) registerNetScan(vm *goja.Runtime) {
	snap := netscan.Snapshot{}
	if s.netScan != nil {
		snap = s.netScan.Snapshot()
	}

	toValue := func(hosts []netscan.Host) goja.Value {
		out := make([]interface{}, 0, len(hosts))
		for _, h := range hosts {
			out = append(out, hostToMap(h))
		}
		return vm.ToValue(out)
	}

	api := map[string]interface{}{
		// info() — { network, ready, scanning, lastScan, durationMs, hostCount }
		//
		// ready is the one a routine must check first: false means no sweep has
		// completed yet, so an empty host list says nothing about whether a
		// tool is present.
		"info": func(goja.FunctionCall) goja.Value {
			return vm.ToValue(map[string]interface{}{
				"network":    snap.Network,
				"ready":      snap.Ready,
				"scanning":   snap.Scanning,
				"lastScan":   snap.LastScan.Format(time.RFC3339),
				"durationMs": snap.DurationMS,
				"hostCount":  len(snap.Hosts),
			})
		},

		// hosts() — every address that answered during the last sweep.
		"hosts": func(goja.FunctionCall) goja.Value {
			return toValue(snap.Hosts)
		},

		// find(substring) — hosts whose response body contains substring.
		// The common case: match a <title> or a product banner.
		"find": func(call goja.FunctionCall) goja.Value {
			if len(call.Arguments) < 1 {
				return toValue(nil)
			}
			needle := call.Arguments[0].String()
			var hits []netscan.Host
			for _, h := range snap.Hosts {
				if strings.Contains(h.Body, needle) {
					hits = append(hits, h)
				}
			}
			return toValue(hits)
		},

		// findMatching(regex) — hosts whose body matches the pattern. An
		// invalid pattern yields no hits rather than throwing, since routines
		// are forbidden from using try/catch.
		"findMatching": func(call goja.FunctionCall) goja.Value {
			if len(call.Arguments) < 1 {
				return toValue(nil)
			}
			re, err := regexp.Compile(call.Arguments[0].String())
			if err != nil {
				return toValue(nil)
			}
			var hits []netscan.Host
			for _, h := range snap.Hosts {
				if re.MatchString(h.Body) {
					hits = append(hits, h)
				}
			}
			return toValue(hits)
		},

		// get(ip[, port]) — a single host record, or null. With port omitted,
		// the first record for that address is returned.
		"get": func(call goja.FunctionCall) goja.Value {
			if len(call.Arguments) < 1 {
				return goja.Null()
			}
			ip := call.Arguments[0].String()
			port := 0
			if len(call.Arguments) > 1 && !goja.IsUndefined(call.Arguments[1]) {
				port = int(call.Arguments[1].ToInteger())
			}
			for _, h := range snap.Hosts {
				if h.IP == ip && (port == 0 || h.Port == port) {
					return vm.ToValue(hostToMap(h))
				}
			}
			return goja.Null()
		},
	}

	vm.Set("netscan", api)
}

func hostToMap(h netscan.Host) map[string]interface{} {
	headers := make(map[string]interface{}, len(h.Headers))
	for k, v := range h.Headers {
		headers[k] = v
	}
	dnsNames := make([]interface{}, 0, len(h.CertDNSNames))
	for _, n := range h.CertDNSNames {
		dnsNames = append(dnsNames, n)
	}
	return map[string]interface{}{
		"ip":           h.IP,
		"port":         h.Port,
		"url":          h.URL,
		"finalUrl":     h.FinalURL,
		"status":       h.Status,
		"body":         h.Body,
		"title":        h.Title(),
		"headers":      headers,
		"tlsTrusted":   h.TLSTrusted,
		"certSubject":  h.CertSubject,
		"certIssuer":   h.CertIssuer,
		"certDnsNames": dnsNames,
		"certNotAfter": h.CertNotAfter,
		"error":        h.Error,
	}
}

// registerHTTPGet installs the `httpGet` global.
//
// The tool-detection system prompt has always documented this helper, but the
// runtime never provided it — and because the prompt also forbids try/catch, a
// generated routine that took the prompt at its word threw on an undefined
// function and failed the whole file. Implementing it makes the documented
// contract true, and gives routines a way to reach a host or port outside the
// periodic sweep.
func registerHTTPGet(vm *goja.Runtime) {
	fail := func(msg string) goja.Value {
		return vm.ToValue(map[string]interface{}{"status": 0, "body": "", "error": msg})
	}

	vm.Set("httpGet", func(call goja.FunctionCall) goja.Value {
		if len(call.Arguments) < 1 {
			return fail("httpGet requires a url")
		}
		url := call.Arguments[0].String()

		timeout := defaultHTTPTimeout
		skipTLS := false
		headers := map[string]string{}

		if len(call.Arguments) > 1 && !goja.IsUndefined(call.Arguments[1]) {
			if opts, ok := call.Arguments[1].Export().(map[string]interface{}); ok {
				if v, ok := opts["timeout"].(int64); ok && v > 0 {
					timeout = time.Duration(v) * time.Millisecond
				}
				if v, ok := opts["timeout"].(float64); ok && v > 0 {
					timeout = time.Duration(v) * time.Millisecond
				}
				if v, ok := opts["skipTLS"].(bool); ok {
					skipTLS = v
				}
				if hs, ok := opts["headers"].(map[string]interface{}); ok {
					for k, v := range hs {
						if sv, ok := v.(string); ok {
							headers[k] = sv
						}
					}
				}
			}
		}
		if timeout > maxHTTPTimeout {
			timeout = maxHTTPTimeout
		}

		client := &http.Client{
			Timeout: timeout,
			Transport: &http.Transport{
				TLSClientConfig:   &tls.Config{InsecureSkipVerify: skipTLS},
				DisableKeepAlives: true,
			},
		}
		if !defaultHTTPRedirect {
			client.CheckRedirect = func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			}
		}

		req, err := http.NewRequest("GET", url, nil)
		if err != nil {
			return fail(err.Error())
		}
		for k, v := range headers {
			req.Header.Set(k, v)
		}

		resp, err := client.Do(req)
		if err != nil {
			return fail(err.Error())
		}
		defer resp.Body.Close()

		body, _ := io.ReadAll(io.LimitReader(resp.Body, defaultHTTPBodyCap))
		return vm.ToValue(map[string]interface{}{
			"status": resp.StatusCode,
			"body":   string(body),
			"error":  "",
		})
	})
}
