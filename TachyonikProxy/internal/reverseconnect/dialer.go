// TachyonikProxy
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

package reverseconnect

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"tachyonik/tachyonikproxy/internal/config"
	"tachyonik/tachyonikproxy/internal/logger"
	"tachyonik/tachyonikproxy/internal/mcpserver"
)

// registerMessage is sent to ToolManager immediately after connecting.
type registerMessage struct {
	Type      string `json:"type"`
	ProxyName string `json:"proxyName"`
}

// Dialer maintains a persistent WebSocket connection to ToolManager,
// forwarding JSON-RPC requests to the local MCP server.
type Dialer struct {
	cfg    *config.Config
	server *mcpserver.Server
	stopCh chan struct{}
	done   chan struct{}
	once   sync.Once
}

// NewDialer creates a new reverse-connect dialer.
func NewDialer(cfg *config.Config, server *mcpserver.Server) *Dialer {
	return &Dialer{
		cfg:    cfg,
		server: server,
		stopCh: make(chan struct{}),
		done:   make(chan struct{}),
	}
}

// Start begins the connection loop in a goroutine.
func (d *Dialer) Start() error {
	go d.run()
	return nil
}

// Stop signals the dialer to disconnect and stop reconnecting.
func (d *Dialer) Stop() {
	d.once.Do(func() {
		close(d.stopCh)
	})
	<-d.done
}

func (d *Dialer) run() {
	defer close(d.done)

	backoff := 5 * time.Second
	maxBackoff := 60 * time.Second

	for {
		select {
		case <-d.stopCh:
			return
		default:
		}

		err := d.connectAndServe()
		if err != nil {
			logger.Warnf("Reverse-connect error: %v (reconnecting in %v)", err, backoff)
		}

		select {
		case <-d.stopCh:
			return
		case <-time.After(backoff):
		}

		// Exponential backoff
		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

func (d *Dialer) connectAndServe() error {
	tlsCfg, err := d.buildTLSConfig()
	if err != nil {
		return fmt.Errorf("TLS config error: %w", err)
	}

	dialer := websocket.Dialer{
		TLSClientConfig:  tlsCfg,
		HandshakeTimeout: 30 * time.Second,
	}

	url := d.cfg.ReverseConnect.ToolManagerURL
	if url == "" {
		return fmt.Errorf("toolmanager_url not configured")
	}
	// The ToolManager link must always be TLS. gorilla/websocket silently
	// ignores TLSClientConfig for a ws:// target and dials in cleartext, so
	// a hand-edited config or a malicious enrollment response could downgrade
	// the connection. Refuse anything but wss:// — mirrors the https:// guard
	// on the self-update manifest/artifact fetches.
	if !strings.HasPrefix(url, "wss://") {
		return fmt.Errorf("toolmanager_url must be wss:// (TLS required), got %q", url)
	}

	logger.Infof("Connecting to ToolManager at %s", url)

	conn, _, err := dialer.Dial(url, nil)
	if err != nil {
		return fmt.Errorf("dial failed: %w", err)
	}
	defer conn.Close()

	// Send register message
	reg := registerMessage{
		Type:      "register",
		ProxyName: d.cfg.Proxy.Name,
	}
	regBytes, _ := json.Marshal(reg)
	if err := conn.WriteMessage(websocket.TextMessage, regBytes); err != nil {
		return fmt.Errorf("failed to send register message: %w", err)
	}

	logger.Infof("Registered with ToolManager as %s", d.cfg.Proxy.Name)

	// Setup ping ticker
	pingTicker := time.NewTicker(30 * time.Second)
	defer pingTicker.Stop()

	// Start goroutine that handles pings and closes the connection on stop
	go func() {
		for {
			select {
			case <-pingTicker.C:
				if err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(10*time.Second)); err != nil {
					return
				}
			case <-d.stopCh:
				// Close the connection so ReadMessage unblocks
				conn.Close()
				return
			}
		}
	}()

	// Read loop: receive JSON-RPC requests, pass to MCP server, write responses back
	for {
		_, message, err := conn.ReadMessage()
		if err != nil {
			// If we're stopping, this is expected
			select {
			case <-d.stopCh:
				return nil
			default:
			}
			return fmt.Errorf("read error: %w", err)
		}

		// Process request through MCP server
		resp := d.server.HandleRequest(message)
		if resp == nil {
			continue // notification, no response needed
		}

		respBytes, err := json.Marshal(resp)
		if err != nil {
			logger.Errorf("Failed to marshal response: %v", err)
			continue
		}

		if err := conn.WriteMessage(websocket.TextMessage, respBytes); err != nil {
			return fmt.Errorf("write error: %w", err)
		}
	}
}

func (d *Dialer) buildTLSConfig() (*tls.Config, error) {
	// Surface the unenrolled case with a clear message before falling through
	// to lower-level filesystem errors from LoadX509KeyPair / ReadFile.
	var missing []string
	if d.cfg.TLS.CACert == "" {
		missing = append(missing, "tls.ca_cert")
	}
	if d.cfg.ReverseConnect.ClientCert == "" {
		missing = append(missing, "reverse_connect.client_cert")
	}
	if d.cfg.ReverseConnect.ClientKey == "" {
		missing = append(missing, "reverse_connect.client_key")
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("missing TLS material (%s) — proxy is not enrolled. "+
			"Run 'tachyonikproxy enroll <enrollment-url>' or 'tachyonikproxy enroll --listen'",
			strings.Join(missing, ", "))
	}

	// Load client certificate for mTLS. Relative paths resolve against the
	// config file's directory so the proxy works from any CWD.
	cert, err := tls.LoadX509KeyPair(d.cfg.AbsPath(d.cfg.ReverseConnect.ClientCert), d.cfg.AbsPath(d.cfg.ReverseConnect.ClientKey))
	if err != nil {
		return nil, fmt.Errorf("failed to load client certificate: %w", err)
	}

	// Load CA cert to verify ToolManager's server cert
	caCert, err := os.ReadFile(d.cfg.AbsPath(d.cfg.TLS.CACert))
	if err != nil {
		return nil, fmt.Errorf("failed to read CA certificate: %w", err)
	}
	caPool := x509.NewCertPool()
	if !caPool.AppendCertsFromPEM(caCert) {
		return nil, fmt.Errorf("failed to parse CA certificate")
	}

	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		RootCAs:      caPool,
		MinVersion:   tls.VersionTLS13,
	}, nil
}
