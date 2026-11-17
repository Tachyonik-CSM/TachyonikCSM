// AssetManager
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// AssetManager's MCP server at POST /api/mcp
// (docs/specs/01-platform-api-catalogue.md §8): built once at startup from the
// embedded API document, one tool per operation it marks `x-mcp` (PA-MCP-01,
// -04). A tool call is made as the API call it stands for, through this
// router again, with the caller's session token (PA-MCP-06). The route is
// for a signed-in user only: it sits behind the session authentication of
// the user routes, and a request carrying the internal service key is
// refused, since an MCP caller is an agent acting for a user (PA-MCP-05).

package handlers

import (
	"fmt"
	"net/http"

	"tachyonik/assetmanager/internal/version"
	"tachyonik/assetmanager/spec"
	"tachyonik/lib/apicatalogue"
	"tachyonik/lib/apimcp"
)

// mcpServer builds the MCP server over router from the embedded document. The
// document is part of the binary and tested, so a failure is a build defect.
func mcpServer(router http.Handler) http.Handler {
	doc, err := apicatalogue.Parse(spec.OpenAPI)
	if err != nil {
		panic(fmt.Sprintf("MCP: the API document cannot be parsed: %v", err))
	}
	srv, err := mcpServerFor(doc, router)
	if err != nil {
		panic(fmt.Sprintf("MCP: %v", err))
	}
	return usersOnly(srv)
}

// mcpServerFor builds the MCP server for doc over router.
func mcpServerFor(doc *apicatalogue.Document, router http.Handler) (*apimcp.Server, error) {
	return apimcp.New(spec.Module, version.Version, doc, router)
}

// usersOnly refuses a request that carries the internal service key: MCP is
// for signed-in users, whose own permissions apply (PA-MCP-05, -06).
func usersOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Internal-Service-Key") != "" {
			respondError(w, http.StatusForbidden, "The MCP server is for signed-in users, not services")
			return
		}
		next.ServeHTTP(w, r)
	})
}
