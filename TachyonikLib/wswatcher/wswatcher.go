// TachyonikLib
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package wswatcher holds the machinery every manager-watching daemon repeats:
// turn an http(s) base URL into its ws(s) /ws form, dial it with the internal
// service key, keep the connection alive, reconnect when it drops, read the
// JSON envelope each manager broadcasts, and answer PING with PONG.
//
// It knows nothing about what the messages mean. A caller supplies OnMessage
// and decides which types matter, which is what lets one implementation serve
// watchers of AIManager, AssetManager and SystemManager alike — they differ in
// the events they care about, never in how the connection is run.
//
// What it does own is the behaviour that is easy to leave out and expensive to
// omit: a read limit, so one oversized frame is not an OOM; a read deadline, so
// a half-open connection becomes a reconnect rather than a goroutine parked in
// ReadMessage forever; a ping handler that extends that deadline; and a Close
// that can be called twice, because a shutdown path that both defers Close and
// calls it is easy to write and a bare close of a channel panics the second
// time.
package wswatcher

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"tachyonik/lib/internal/safehttp"
	"tachyonik/lib/logger"
)

const (
	// MaxMessageBytes bounds a single inbound frame. gorilla's default read
	// limit is unlimited, so without this one oversized message is an OOM. The
	// managers' own reads are far smaller, so this is ample.
	MaxMessageBytes = 1 << 20 // 1 MiB

	// ReadTimeout must exceed a manager's ping period (54s in their websocket
	// clients) with room for one missed ping.
	ReadTimeout = 90 * time.Second

	// WriteTimeout bounds a pong or close write against a peer that has
	// stopped reading.
	WriteTimeout = 10 * time.Second

	// DefaultReconnectDelay is how long to wait before redialling.
	DefaultReconnectDelay = 5 * time.Second
)

// Message is the envelope every manager broadcasts in.
type Message struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// Config describes one watched connection.
type Config struct {
	// Name appears in log lines — "AIManager", "AssetManager". Purely for
	// reading a log, never for dispatch.
	Name string
	// BaseURL is the manager's http(s) URL; the ws(s) form and the path are
	// derived from it.
	BaseURL string
	// ServiceKey is sent as X-Internal-Service-Key when non-empty.
	ServiceKey string
	// Path defaults to "/ws".
	Path string
	// OnConnect fires on every successful (re)connect, in its own goroutine.
	//
	// This is the resync hook, and it is not optional decoration: a connection
	// established before the data existed, or re-established after a manager
	// restart, has missed whatever happened in between. Treating a fresh
	// connection as "reload everything" is what keeps a daemon from sitting on
	// stale state until the next live event happens along.
	OnConnect func()
	// OnMessage receives every decoded frame except PING, which is answered
	// here. It runs on the read loop, so a handler that blocks stops reading —
	// callers that do slow work hand off to a goroutine.
	OnMessage func(Message)
	// ReconnectDelay defaults to DefaultReconnectDelay.
	ReconnectDelay time.Duration
}

// Watcher maintains one connection described by a Config.
type Watcher struct {
	cfg Config

	mu        sync.Mutex
	conn      *websocket.Conn
	done      chan struct{}
	closeOnce sync.Once
}

// New creates a watcher. Nothing is dialled until Start.
func New(cfg Config) *Watcher {
	if cfg.Path == "" {
		cfg.Path = "/ws"
	}
	if cfg.ReconnectDelay <= 0 {
		cfg.ReconnectDelay = DefaultReconnectDelay
	}
	if cfg.Name == "" {
		cfg.Name = "manager"
	}
	if safehttp.CredentialExposed(cfg.BaseURL, cfg.ServiceKey != "") {
		logger.Warnf("%s watcher configured with an internal service key over a non-TLS URL (%s) — the key will be sent in cleartext",
			cfg.Name, cfg.BaseURL)
	}
	return &Watcher{cfg: cfg, done: make(chan struct{})}
}

// URL is the ws(s) address this watcher dials.
func (w *Watcher) URL() string {
	wsURL := w.cfg.BaseURL
	if strings.HasPrefix(wsURL, "https://") {
		wsURL = "wss://" + strings.TrimPrefix(wsURL, "https://")
	} else if strings.HasPrefix(wsURL, "http://") {
		wsURL = "ws://" + strings.TrimPrefix(wsURL, "http://")
	}

	u, err := url.Parse(wsURL)
	if err != nil {
		return wsURL + w.cfg.Path
	}
	u.Path = w.cfg.Path
	return u.String()
}

// Start runs the connection loop in its own goroutine.
func (w *Watcher) Start() {
	go w.run()
}

func (w *Watcher) run() {
	for {
		select {
		case <-w.done:
			return
		default:
		}

		if err := w.connect(); err != nil {
			logger.Errorf("Failed to connect to %s WebSocket: %v", w.cfg.Name, err)
			logger.Infof("Retrying in %v...", w.cfg.ReconnectDelay)

			select {
			case <-w.done:
				return
			case <-time.After(w.cfg.ReconnectDelay):
				continue
			}
		}

		if w.cfg.OnConnect != nil {
			logger.Infof("%s WebSocket (re)connected — resyncing", w.cfg.Name)
			go w.cfg.OnConnect()
		}

		w.readLoop()

		select {
		case <-w.done:
			return
		default:
			logger.Warnf("%s WebSocket connection lost, reconnecting...", w.cfg.Name)
		}
	}
}

func (w *Watcher) connect() error {
	wsURL := w.URL()
	logger.Infof("Connecting to %s WebSocket: %s", w.cfg.Name, wsURL)

	header := http.Header{}
	if w.cfg.ServiceKey != "" {
		header.Set("X-Internal-Service-Key", w.cfg.ServiceKey)
	}
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, header)
	if err != nil {
		return err
	}

	conn.SetReadLimit(MaxMessageBytes)
	conn.SetReadDeadline(time.Now().Add(ReadTimeout))
	// Replaces gorilla's default ping handler, which replies with a pong but
	// does not extend the read deadline — without the extension a connection
	// carrying nothing but pings would time out.
	conn.SetPingHandler(func(appData string) error {
		conn.SetReadDeadline(time.Now().Add(ReadTimeout))
		conn.SetWriteDeadline(time.Now().Add(WriteTimeout))
		if err := conn.WriteMessage(websocket.PongMessage, []byte(appData)); err != nil && err != websocket.ErrCloseSent {
			return err
		}
		return nil
	})

	w.mu.Lock()
	w.conn = conn
	w.mu.Unlock()

	logger.Infof("Connected to %s WebSocket", w.cfg.Name)
	return nil
}

func (w *Watcher) readLoop() {
	for {
		select {
		case <-w.done:
			return
		default:
		}

		w.mu.Lock()
		conn := w.conn
		w.mu.Unlock()
		if conn == nil {
			return
		}

		_, raw, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				logger.Errorf("%s WebSocket read error: %v", w.cfg.Name, err)
			}
			return
		}
		conn.SetReadDeadline(time.Now().Add(ReadTimeout))

		var msg Message
		if err := json.Unmarshal(raw, &msg); err != nil {
			// Noise on the wire is not a reason to drop the connection.
			logger.Debugf("Failed to parse %s WebSocket message: %v", w.cfg.Name, err)
			continue
		}

		if msg.Type == "PING" {
			w.pong()
			continue
		}
		if w.cfg.OnMessage != nil {
			w.cfg.OnMessage(msg)
		}
	}
}

// pong answers the application-level PING the managers send inside the JSON
// envelope, which is separate from the protocol-level ping the dialer handles.
func (w *Watcher) pong() {
	data, err := json.Marshal(Message{Type: "PONG"})
	if err != nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.conn != nil {
		w.conn.SetWriteDeadline(time.Now().Add(WriteTimeout))
		w.conn.WriteMessage(websocket.TextMessage, data)
	}
}

// Close stops the watcher and closes the connection. Safe to call more than
// once; subsequent calls are no-ops and return nil.
func (w *Watcher) Close() error {
	var err error
	w.closeOnce.Do(func() {
		close(w.done)

		w.mu.Lock()
		defer w.mu.Unlock()
		if w.conn != nil {
			w.conn.SetWriteDeadline(time.Now().Add(WriteTimeout))
			w.conn.WriteMessage(websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
			err = w.conn.Close()
			w.conn = nil
		}
	})
	return err
}
