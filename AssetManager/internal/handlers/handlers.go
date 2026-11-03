// AssetManager
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package handlers serves the AssetManager HTTP API: the assets, vulnerabilities
// and detections a user holds, the filters that narrow them, and their aggregate
// statistics. Imports reach it through internal endpoints, as do the whole-user
// operations that account deletion and anonymous-to-registered transfer need.
//
// It computes two of the four Tachyonik sub-scores — Exposure, from the
// severities found on a user's assets, and Coverage, from how much of the
// organisation's declared estate is actually represented.
//
// Quota enforcement lives here too, and its audit events are coalesced per user,
// source and kind: an import that blows past a limit keeps posting every parsed
// item, so the over-limit rejections arrive as a burst and are reported as one
// summary entry rather than hundreds.
//
// The package also carries the cross-cutting request plumbing — authentication
// and tenancy middleware, Prometheus metrics, health and readiness — and
// broadcasts changes over the WebSocket hub so an open UI updates without
// polling.
package handlers

import (
	"net/http"
	"strings"
	"sync/atomic"

	"github.com/gorilla/mux"
	"github.com/gorilla/websocket"
	"tachyonik/assetmanager/internal/aimanager"
	"tachyonik/assetmanager/internal/config"
	"tachyonik/assetmanager/internal/database"
	"tachyonik/assetmanager/internal/rscmanager"
	"tachyonik/assetmanager/internal/systemmanager"
	ws "tachyonik/assetmanager/internal/websocket"
	"tachyonik/lib/logger"
)

// newWSUpgrader builds a WebSocket upgrader whose CheckOrigin rejects
// cross-origin browser connections not on the configured allow-list — this
// closes the "any malicious web page can open the socket" vector. A request
// with no Origin header (non-browser client) is allowed through here; the
// real gate against unauthenticated non-browser clients is JWT validation in
// handleWebSocket (tracked separately). (SECURITY: CRIT-3)
func (h *Handler) newWSUpgrader() websocket.Upgrader {
	return websocket.Upgrader{
		ReadBufferSize:  1024,
		WriteBufferSize: 1024,
		CheckOrigin: func(r *http.Request) bool {
			origin := r.Header.Get("Origin")
			if origin == "" {
				return true
			}
			for _, allowed := range h.config.CORS.AllowedOrigins {
				if origin == allowed {
					return true
				}
			}
			logger.Warnf("WebSocket: rejecting connection from disallowed origin %q", origin)
			return false
		},
	}
}

// Handler holds dependencies for HTTP handlers
type Handler struct {
	db                  *database.DB
	hub                 *ws.Hub
	config              *config.Config
	rscManagerClient    *rscmanager.Client
	systemManagerClient *systemmanager.Client
	// accounts refuses a session whose account was deactivated or deleted;
	// nil without a SystemManager client (tests only).
	accounts *accountChecker
	// aiManagerClient reads this appliance's per-appliance license (instance-wide
	// host-asset cap) from the local AIManager. Nil when no AIManager URL is
	// configured (e.g. on the SaaS) — then the instance-wide cap is disabled.
	aiManagerClient *aimanager.Client
	// limitAudit coalesces per-item "Limit Exceeded" audit events into one
	// summary per (user, source, kind) — see limitaudit.go.
	limitAudit *limitAuditAggregator
	// ready flips to true once startup work has finished and /ready
	// should begin reporting 200. See health.go.
	ready atomic.Bool
}

// MarkReady flips the readiness gate. Called by main() at the end of
// startup, after every prerequisite has been wired.
func (h *Handler) MarkReady() {
	h.ready.Store(true)
}

// sharedWorkspace reports whether this instance runs in Appliance mode, where
// the whole instance is one shared workspace and per-user quotas are enforced
// instance-wide (all users' data counts against one budget).
func (h *Handler) sharedWorkspace() bool {
	return h.config.Instance.Mode == config.ModeAppliance
}

// handleHealth — liveness: 200 while the process is up.
func (h *Handler) handleHealth(w http.ResponseWriter, _ *http.Request) {
	respondJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleReady — readiness: 200 only after MarkReady() flipped the gate.
func (h *Handler) handleReady(w http.ResponseWriter, _ *http.Request) {
	if h.ready.Load() {
		respondJSON(w, http.StatusOK, map[string]string{"status": "ready"})
		return
	}
	respondJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "starting"})
}

// NewHandler creates a new handler instance
func NewHandler(db *database.DB, hub *ws.Hub, cfg *config.Config, rscManagerClient *rscmanager.Client, systemManagerClient *systemmanager.Client) *Handler {
	h := &Handler{
		db:                  db,
		hub:                 hub,
		config:              cfg,
		rscManagerClient:    rscManagerClient,
		systemManagerClient: systemManagerClient,
		// nil when AIManager.URL is empty (no instance-wide cap).
		aiManagerClient: aimanager.NewClient(cfg.AIManager.URL, cfg.AIManager.InternalServiceKey),
	}
	if systemManagerClient != nil {
		h.accounts = newAccountChecker(systemManagerClient)
	}
	h.limitAudit = newLimitAuditAggregator(h.emitAudit)
	return h
}

// Shutdown flushes any pending coalesced limit-exceeded audit summaries.
// Call during graceful shutdown so a burst still in flight isn't lost.
func (h *Handler) Shutdown() {
	h.limitAudit.FlushAll()
}

// emitAudit posts an audit-trail event to SystemManager. Best-effort —
// failures are logged but never block the calling request.
func (h *Handler) emitAudit(userID int64, level, message string) {
	if h.systemManagerClient == nil || userID == 0 {
		return
	}
	if err := h.systemManagerClient.CreateAuditEvent(userID, level, "AssetManager", message); err != nil {
		logger.Warnf("audit event emit failed: %v", err)
	}
}

// emitTotalAssets, emitTotalVulnerabilities, emitTotalDetections each
// recompute the user's total and ship it to SystemManager's stats
// endpoint. Best-effort, run in a goroutine so the calling request
// returns promptly. Throttled at the client.
func (h *Handler) emitTotalAssets(userID int64) {
	if h.systemManagerClient == nil || userID == 0 {
		return
	}
	go func() {
		count, err := h.db.CountAssetsByUserWithFilter(userID, nil)
		if err != nil {
			logger.Warnf("failed to count assets for stats: %v", err)
			return
		}
		if err := h.systemManagerClient.CreateStatsEntry(userID, "totalAssets", float64(count)); err != nil {
			logger.Warnf("stats emit (totalAssets) failed: %v", err)
		}
	}()
}

func (h *Handler) emitTotalVulnerabilities(userID int64) {
	if h.systemManagerClient == nil || userID == 0 {
		return
	}
	go func() {
		count, err := h.db.CountVulnerabilitiesByUserWithFilter(userID, nil)
		if err != nil {
			logger.Warnf("failed to count vulnerabilities for stats: %v", err)
			return
		}
		if err := h.systemManagerClient.CreateStatsEntry(userID, "totalVulnerabilities", float64(count)); err != nil {
			logger.Warnf("stats emit (totalVulnerabilities) failed: %v", err)
		}
	}()
}

func (h *Handler) emitTotalDetections(userID int64) {
	if h.systemManagerClient == nil || userID == 0 {
		return
	}
	go func() {
		count, err := h.db.CountDetectionsByUserWithFilter(userID, nil)
		if err != nil {
			logger.Warnf("failed to count detections for stats: %v", err)
			return
		}
		if err := h.systemManagerClient.CreateStatsEntry(userID, "totalDetections", float64(count)); err != nil {
			logger.Warnf("stats emit (totalDetections) failed: %v", err)
		}
	}()
}

// SetupRoutes configures all HTTP routes
func (h *Handler) SetupRoutes() http.Handler {
	r := mux.NewRouter()

	// Apply middleware
	r.Use(CORSMiddleware(h.config.CORS.AllowedOrigins))
	r.Use(LoggingMiddleware)
	r.Use(MetricsMiddleware)

	// Liveness and readiness probes (no auth — for orchestrator).
	r.HandleFunc("/health", h.handleHealth).Methods("GET", "OPTIONS")
	r.HandleFunc("/ready", h.handleReady).Methods("GET", "OPTIONS")
	// Prometheus scrape endpoint. No auth — lock down at the network layer.
	r.Handle("/metrics", MetricsHandler()).Methods("GET")

	// API routes - require authentication
	api := r.PathPrefix("/api").Subrouter()
	api.Use(BodyLimitMiddleware)
	api.Use(AuthMiddleware(h.config, h.accounts))
	api.HandleFunc("/assets/stats", h.GetAssetStats).Methods("GET", "OPTIONS")
	api.HandleFunc("/assets", h.ListAssets).Methods("GET", "OPTIONS")
	api.HandleFunc("/assets", h.CreateAsset).Methods("POST", "OPTIONS")
	api.HandleFunc("/assets", h.DeleteAssetsByIDs).Methods("DELETE", "OPTIONS")
	api.HandleFunc("/assets/by-source", h.DeleteAssetsBySource).Methods("DELETE", "OPTIONS")
	api.HandleFunc("/assets/by-source", h.ClearAssetSourcesBySource).Methods("PATCH", "OPTIONS")
	api.HandleFunc("/assets/stale", h.DeleteStaleAssetsBySource).Methods("DELETE", "OPTIONS")
	api.HandleFunc("/assets/scan-coverage", h.ApplyScanCoverage).Methods("POST", "OPTIONS")
	// One asset by id, owner-scoped; a PATCH may change the type only.
	api.HandleFunc("/assets/{id:[0-9]+}", h.GetAsset).Methods("GET", "OPTIONS")
	api.HandleFunc("/assets/{id:[0-9]+}", h.UpdateAsset).Methods("PATCH", "OPTIONS")
	api.HandleFunc("/vulnerabilities", h.ListVulnerabilities).Methods("GET", "OPTIONS")
	api.HandleFunc("/vulnerabilities", h.CreateVulnerability).Methods("POST", "OPTIONS")
	api.HandleFunc("/vulnerabilities", h.DeleteVulnerabilitiesByIDs).Methods("DELETE", "OPTIONS")
	api.HandleFunc("/vulnerabilities/by-source", h.DeleteVulnerabilitiesBySource).Methods("DELETE", "OPTIONS")
	api.HandleFunc("/vulnerabilities/by-source", h.ClearVulnerabilitySourcesBySource).Methods("PATCH", "OPTIONS")
	api.HandleFunc("/vulnerabilities/stale", h.DeleteStaleVulnerabilitiesBySource).Methods("DELETE", "OPTIONS")
	api.HandleFunc("/detections", h.ListDetections).Methods("GET", "OPTIONS")
	api.HandleFunc("/detections", h.CreateDetection).Methods("POST", "OPTIONS")
	api.HandleFunc("/detections", h.DeleteDetectionsByIDs).Methods("DELETE", "OPTIONS")
	api.HandleFunc("/detections/by-source", h.DeleteDetectionsBySource).Methods("DELETE", "OPTIONS")
	api.HandleFunc("/detections/by-source", h.ClearDetectionSourcesBySource).Methods("PATCH", "OPTIONS")
	api.HandleFunc("/detections/stale", h.DeleteStaleDetectionsBySource).Methods("DELETE", "OPTIONS")

	// Internal service routes (require internal service key)
	internalRoutes := api.PathPrefix("/internal").Subrouter()
	internalRoutes.Use(RequireInternalService(h.config))
	internalRoutes.HandleFunc("/organisation/recalculate-score", h.RecalculateOrganisationScore).Methods("POST", "OPTIONS")
	internalRoutes.HandleFunc("/users/{id:[0-9]+}/data", h.DeleteUserData).Methods("DELETE", "OPTIONS")
	internalRoutes.HandleFunc("/users/ids", h.ListUserIDs).Methods("GET", "OPTIONS")
	internalRoutes.HandleFunc("/users/{id:[0-9]+}/data/transfer", h.TransferUserData).Methods("PATCH", "OPTIONS")

	// WebSocket route
	r.HandleFunc("/ws", h.handleWebSocket)

	return r
}

// handleWebSocket handles WebSocket upgrade requests. The connection is
// authenticated with a JWT carried as the WebSocket subprotocol
// (Sec-WebSocket-Protocol). Browsers can't set the Authorization header on a
// WebSocket, and a subprotocol keeps the token out of the URL — and therefore
// out of nginx/proxy access logs — unlike a query parameter. The caller's
// visible-user set is resolved at connect time and used by the hub to scope
// which tenants' events this client receives. (SECURITY: CRIT-3)
func (h *Handler) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	// Service-to-service path: a trusted internal daemon presents the shared
	// internal-service key as a request header (Go dialers can set headers;
	// browsers can't on a WebSocket) and receives every event, unscoped. This
	// is how the asset-change watchers consume the feed. (SECURITY: CRIT-3)
	if internalKeyValid(h.config, r.Header.Get("X-Internal-Service-Key")) {
		upgrader := h.newWSUpgrader()
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			logger.Errorf("WebSocket upgrade error: %v", err)
			return
		}
		client := ws.NewInternalClient(h.hub, conn)
		h.hub.Register(client)
		go client.WritePump()
		go client.ReadPump()
		return
	}

	// Browser path: the client offers exactly the JWT as its single
	// subprotocol. Header lookup is canonicalised, so "Sec-Websocket-Protocol"
	// matches.
	tokenString := strings.TrimSpace(r.Header.Get("Sec-Websocket-Protocol"))
	if tokenString == "" {
		logger.Warnf("WebSocket: rejecting unauthenticated connection (no token offered as Sec-WebSocket-Protocol) from %s origin %q", r.RemoteAddr, r.Header.Get("Origin"))
		http.Error(w, "Authentication required", http.StatusUnauthorized)
		return
	}
	claims, err := ParseJWT(h.config, tokenString)
	if err != nil {
		logger.Warnf("WebSocket: rejecting connection with invalid/expired token from %s origin %q: %v", r.RemoteAddr, r.Header.Get("Origin"), err)
		http.Error(w, "Invalid or expired token", http.StatusUnauthorized)
		return
	}
	if status, msg := accountRefusal(h.accounts, claims.UserID); status != 0 {
		http.Error(w, msg, status)
		return
	}

	visible := h.VisibleUserIDsForUser(claims.UserID)

	upgrader := h.newWSUpgrader()
	// Echo the offered subprotocol back so the browser's handshake completes.
	conn, err := upgrader.Upgrade(w, r, http.Header{"Sec-Websocket-Protocol": {tokenString}})
	if err != nil {
		logger.Errorf("WebSocket upgrade error: %v", err)
		return
	}

	client := ws.NewClient(h.hub, conn, claims.UserID, visible)
	h.hub.Register(client)

	// Start client's read and write pumps
	go client.WritePump()
	go client.ReadPump()
}
