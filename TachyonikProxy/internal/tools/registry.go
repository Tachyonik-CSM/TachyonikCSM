// TachyonikProxy
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

package tools

import (
	"encoding/json"
	"fmt"
	"sync"

	"tachyonik/lib/logger"
	"tachyonik/tachyonikproxy/internal/config"
)

// MCPTool represents a tool in MCP protocol format.
type MCPTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// Registry manages available tools from local config and upstream MCP servers.
type Registry struct {
	mu         sync.RWMutex
	localTools map[string]*config.ToolConfig
	proxyTools map[string]*ProxyTool
}

// ProxyTool represents a tool proxied from an upstream MCP server.
type ProxyTool struct {
	ServerName string
	Tool       MCPTool
}

// NewRegistry creates a new tool registry from the configuration.
func NewRegistry(tools []config.ToolConfig, mcpServers []config.MCPServerConfig) *Registry {
	r := &Registry{
		localTools: make(map[string]*config.ToolConfig),
		proxyTools: make(map[string]*ProxyTool),
	}

	for i := range tools {
		r.localTools[tools[i].Name] = &tools[i]
		logger.Infof("Registered local tool: %s", tools[i].Name)
	}

	// Proxy tools from upstream MCP servers would be fetched asynchronously
	for _, srv := range mcpServers {
		logger.Infof("Upstream MCP server configured: %s at %s (tools will be fetched on first request)", srv.Name, srv.URL)
	}

	return r
}

// ListTools returns all available tools in MCP format.
func (r *Registry) ListTools() []MCPTool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var tools []MCPTool

	for _, t := range r.localTools {
		schema := t.ArgsSchema.RawMessage()
		if len(schema) == 0 {
			schema = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		tools = append(tools, MCPTool{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: schema,
		})
	}

	for _, pt := range r.proxyTools {
		tools = append(tools, pt.Tool)
	}

	return tools
}

// CallTool executes a tool by name with the given arguments.
func (r *Registry) CallTool(name string, args map[string]interface{}) (*ToolResult, error) {
	r.mu.RLock()
	tool, ok := r.localTools[name]
	r.mu.RUnlock()

	if ok {
		return ExecuteTool(tool, args)
	}

	return nil, fmt.Errorf("tool %q not found", name)
}

// UpdateTools replaces the local tool registry.
func (r *Registry) UpdateTools(tools []config.ToolConfig) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.localTools = make(map[string]*config.ToolConfig)
	for i := range tools {
		r.localTools[tools[i].Name] = &tools[i]
	}
}
