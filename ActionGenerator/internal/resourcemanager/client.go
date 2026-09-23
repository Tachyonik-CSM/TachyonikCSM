// ActionGenerator
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package resourcemanager is the read-only client for the ResourceManager API.
// It supplies the sources and tools a user has, and their source count against
// quota, all of which rules reason about.
//
// The request mechanics live in TachyonikLib's restclient: joining the path,
// attaching the service key, checking the status and decoding. What stays here
// is this service's wire types and paths.
package resourcemanager

import (
	"fmt"
	"net/http"
	"time"

	"tachyonik/lib/restclient"
)

// Client handles communication with the ResourceManager API.
type Client struct {
	rc *restclient.Client
}

// Source represents a source entry in the system
type Source struct {
	ID         int64  `json:"id"`
	UserID     int64  `json:"userId"`
	Filename   string `json:"filename"`
	SourceType string `json:"sourceType"`
	Status     string `json:"status"`
	CreatedAt  string `json:"createdAt"`
}

// sourceListResponse is ResourceManager's reply to the sources endpoint. The
// envelope carries the quota alongside the entries, which is why the count and
// the list are one call rather than two.
type sourceListResponse struct {
	Entries    []Source `json:"entries"`
	Total      int      `json:"total"`
	MaxEntries int      `json:"maxEntries"`
}

// Tool is one row of a user's tool inventory.
//
// ToolID is the AIManager ToolOverview ID this row maps to (the tool's
// catalogue identity, e.g. nmap=7). Nullable: rows for unmanaged tools
// (detected on disk but no overview match) leave it nil. Was renamed
// from TypeID after the schema rekey from rule reference to overview
// reference — RM now emits this as "toolId".
type Tool struct {
	ID     int64  `json:"id"`
	ToolID *int64 `json:"toolId"`
	Name   string `json:"name"`
	UserID int64  `json:"userId"`
}

// NewClient creates a new ResourceManager API client
func NewClient(baseURL, internalServiceKey string) *Client {
	return &Client{rc: restclient.New(baseURL, internalServiceKey, 10*time.Second)}
}

// sources fetches the sources endpoint once. Both the list and the count are
// read from this one reply — they were separate copies of the same request
// until the clients moved onto restclient.
func (c *Client) sources(userID int64) (*sourceListResponse, error) {
	var result sourceListResponse
	if err := c.rc.JSON("GET", fmt.Sprintf("/api/sources?userId=%d", userID), nil, &result, http.StatusOK); err != nil {
		return nil, err
	}
	return &result, nil
}

// GetSourcesForUser retrieves all sources for a specific user
func (c *Client) GetSourcesForUser(userID int64) ([]Source, error) {
	result, err := c.sources(userID)
	if err != nil {
		return nil, err
	}
	return result.Entries, nil
}

// GetSourceCount returns how many sources the user has and how many they are
// allowed, so a rule can reason about the quota.
func (c *Client) GetSourceCount(userID int64) (int, int, error) {
	result, err := c.sources(userID)
	if err != nil {
		return 0, 0, err
	}
	return result.Total, result.MaxEntries, nil
}

// GetToolsForUser retrieves all tools configured by the specified user.
func (c *Client) GetToolsForUser(userID int64) ([]Tool, error) {
	var result struct {
		Tools []Tool `json:"tools"`
		Total int    `json:"total"`
	}
	if err := c.rc.JSON("GET", fmt.Sprintf("/api/tools?userId=%d", userID), nil, &result, http.StatusOK); err != nil {
		return nil, err
	}
	return result.Tools, nil
}
