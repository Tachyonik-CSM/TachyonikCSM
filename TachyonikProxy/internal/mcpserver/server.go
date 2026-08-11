// TachyonikProxy
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

package mcpserver

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	"tachyonik/lib/logger"
	"tachyonik/tachyonikproxy/internal/config"
	"tachyonik/tachyonikproxy/internal/tools"
	"tachyonik/tachyonikproxy/internal/toolscan"
)

// JSON-RPC 2.0 types
type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  interface{}     `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// JSON-RPC 2.0 error codes.
const (
	codeParseError     = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
)

// okResp builds a successful JSON-RPC response.
func okResp(id json.RawMessage, result interface{}) *Response {
	return &Response{JSONRPC: "2.0", ID: id, Result: result}
}

// errResp builds a JSON-RPC error response.
func errResp(id json.RawMessage, code int, msg string) *Response {
	return &Response{JSONRPC: "2.0", ID: id, Error: &RPCError{Code: code, Message: msg}}
}

// MCP types
type ServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type Capabilities struct {
	Tools *ToolsCapability `json:"tools,omitempty"`
}

type ToolsCapability struct {
	ListChanged bool `json:"listChanged"`
}

type InitializeResult struct {
	ProtocolVersion string       `json:"protocolVersion"`
	Capabilities    Capabilities `json:"capabilities"`
	ServerInfo      ServerInfo   `json:"serverInfo"`
}

type ToolsListResult struct {
	Tools []tools.MCPTool `json:"tools"`
}

type ToolCallParams struct {
	Name      string                 `json:"name"`
	Arguments map[string]interface{} `json:"arguments"`
}

type ToolCallResult struct {
	Content []interface{} `json:"content"`
	IsError bool          `json:"isError,omitempty"`
}

type ContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type ResourceContent struct {
	URI      string `json:"uri"`
	MimeType string `json:"mimeType"`
	Blob     string `json:"blob"`
}

type ResourceContentBlock struct {
	Type     string          `json:"type"`
	Resource ResourceContent `json:"resource"`
}

type ConfigGetResult struct {
	Tools             []config.ToolConfig      `json:"tools"`
	MCPServers        []config.MCPServerConfig `json:"mcpServers"`
	AllowRemoteConfig bool                     `json:"allowRemoteConfig"`
}

type ConfigUpdateParams struct {
	Tools      []config.ToolConfig      `json:"tools,omitempty"`
	MCPServers []config.MCPServerConfig `json:"mcpServers,omitempty"`
}

type ToolsScanResult struct {
	Tools []toolscan.ToolResult `json:"tools"`
}

// Server implements the MCP JSON-RPC server logic.
type Server struct {
	config      *config.Config
	registry    *tools.Registry
	version     string
	initialized atomic.Bool
	mu          sync.RWMutex
}

// NewServer creates a new MCP server. version is the proxy's binary
// version, threaded through from cmd/server's main.version (set via
// -ldflags at build time). It is reported on the wire in
// initialize.serverInfo.version so ToolManager — and therefore the
// ResourceManager `proxies.version` column — sees what's actually
// running, not a hard-coded placeholder.
func NewServer(cfg *config.Config, registry *tools.Registry, version string) *Server {
	if version == "" {
		version = "dev"
	}
	return &Server{
		config:   cfg,
		registry: registry,
		version:  version,
	}
}

// HandleRequest processes a single JSON-RPC request and returns a response.
func (s *Server) HandleRequest(raw []byte) *Response {
	var req Request
	if err := json.Unmarshal(raw, &req); err != nil {
		return errResp(nil, codeParseError, "Parse error")
	}

	if req.JSONRPC != "2.0" {
		return errResp(req.ID, codeInvalidRequest, "Invalid Request: jsonrpc must be 2.0")
	}

	logger.Debugf("MCP request: method=%s", req.Method)

	switch req.Method {
	case "initialize":
		return s.handleInitialize(req)
	case "notifications/initialized":
		return nil // notification, no response
	case "tools/list":
		return s.handleToolsList(req)
	case "tools/call":
		return s.handleToolsCall(req)
	case "tools/scan":
		return s.handleToolsScan(req)
	case "config/get":
		return s.handleConfigGet(req)
	case "config/update":
		return s.handleConfigUpdate(req)
	case "ping":
		return okResp(req.ID, map[string]string{})
	default:
		return errResp(req.ID, codeMethodNotFound, fmt.Sprintf("Method not found: %s", req.Method))
	}
}

func (s *Server) handleInitialize(req Request) *Response {
	s.initialized.Store(true)
	return okResp(req.ID, InitializeResult{
		ProtocolVersion: "2024-11-05",
		Capabilities: Capabilities{
			Tools: &ToolsCapability{ListChanged: false},
		},
		ServerInfo: ServerInfo{
			Name:    s.config.Proxy.Name,
			Version: s.version,
		},
	})
}

func (s *Server) handleToolsList(req Request) *Response {
	toolList := s.registry.ListTools()
	return okResp(req.ID, ToolsListResult{Tools: toolList})
}

func (s *Server) handleToolsCall(req Request) *Response {
	var params ToolCallParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return errResp(req.ID, codeInvalidParams, "Invalid params")
	}

	result, err := s.registry.CallTool(params.Name, params.Arguments)
	if err != nil {
		return okResp(req.ID, ToolCallResult{
			Content: []interface{}{ContentBlock{Type: "text", Text: err.Error()}},
			IsError: true,
		})
	}

	content := []interface{}{ContentBlock{Type: "text", Text: result.Content}}

	// Append resource content blocks for output files
	for _, f := range result.OutputFiles {
		content = append(content, ResourceContentBlock{
			Type: "resource",
			Resource: ResourceContent{
				URI:      "file://" + f.Filename,
				MimeType: f.MimeType,
				Blob:     f.Data,
			},
		})
	}

	return okResp(req.ID, ToolCallResult{
		Content: content,
		IsError: result.IsError,
	})
}

type ToolsScanParams struct {
	Routines []toolscan.RoutineInput `json:"routines"`
}

func (s *Server) handleToolsScan(req Request) *Response {
	var params ToolsScanParams
	if req.Params != nil {
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return errResp(req.ID, codeInvalidParams, "Invalid params")
		}
	}

	scanner := toolscan.New()
	results := scanner.Scan(params.Routines)

	detectedCount := 0
	for _, r := range results {
		if r.Detected {
			detectedCount++
		}
	}

	logger.Infof("Tool scan completed: %d results (%d detected) from %d routines", len(results), detectedCount, len(params.Routines))
	return okResp(req.ID, ToolsScanResult{Tools: results})
}

func (s *Server) handleConfigGet(req Request) *Response {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return okResp(req.ID, ConfigGetResult{
		Tools:             s.config.Tools,
		MCPServers:        s.config.MCPServers,
		AllowRemoteConfig: s.config.AllowRemoteConfig,
	})
}

// validatePushedTools enforces a minimum security floor on tools delivered
// via a remote config/update, so a push cannot weaken local execution policy.
func validatePushedTools(tools []config.ToolConfig) error {
	for _, t := range tools {
		if strings.TrimSpace(t.Name) == "" {
			return fmt.Errorf("rejected config update: a tool has an empty name")
		}
		if strings.TrimSpace(t.Command) == "" {
			return fmt.Errorf("rejected config update: tool %q has an empty command", t.Name)
		}
		if strings.TrimSpace(t.AllowedChars) == "" {
			return fmt.Errorf("rejected config update: tool %q must set allowed_chars (a remote push may not disable argument validation)", t.Name)
		}
	}
	return nil
}

func (s *Server) handleConfigUpdate(req Request) *Response {
	if !s.config.AllowRemoteConfig {
		return errResp(req.ID, codeInvalidRequest, "Remote configuration updates are disabled")
	}

	var params ConfigUpdateParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return errResp(req.ID, codeInvalidParams, "Invalid params")
	}

	// Validation floor for remotely-pushed tools. A remote push must not be
	// able to register a tool that disables argument validation (empty
	// allowed_chars) or runs an empty/blank command — combined with flag
	// handling that would widen this into arbitrary execution.
	if params.Tools != nil {
		if err := validatePushedTools(params.Tools); err != nil {
			return errResp(req.ID, codeInvalidParams, err.Error())
		}
	}

	s.mu.Lock()
	if params.Tools != nil {
		s.config.Tools = params.Tools
		s.registry.UpdateTools(params.Tools)
	}
	if params.MCPServers != nil {
		s.config.MCPServers = params.MCPServers
	}
	s.mu.Unlock()

	// Persist config — to the same resolved path the config was loaded
	// from, never a CWD-relative fallback (a remote config push must not
	// scatter config.yaml copies into the daemon's working directory).
	configPath := config.GetConfigPath()
	if err := config.SaveConfig(s.config, configPath); err != nil {
		logger.Errorf("Failed to save config: %v", err)
	}

	logger.Infof("Configuration updated remotely")
	return okResp(req.ID, map[string]string{"status": "updated"})
}
