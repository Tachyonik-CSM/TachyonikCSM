// SourceAnalyser
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package resourcemanager is an outbound HTTP client for the ResourceManager
// REST API. It lists the sources awaiting analysis, downloads their content,
// and writes the identified type and status back. The request plumbing lives in
// internal/restclient; what remains here is the wire types and one line per
// endpoint, plus the bounded file download.
package resourcemanager

import (
	"fmt"
	"io"
	"net/http"
	"time"

	"tachyonik/sourceanalyser/internal/restclient"
)

// Client handles communication with ResourceManager API
type Client struct {
	rc *restclient.Client
}

// NewClient creates a new API client
func NewClient(baseURL string, internalServiceKey string) *Client {
	return &Client{rc: restclient.New(baseURL, internalServiceKey, 10*time.Second)}
}

// Source represents a source from the API
type Source struct {
	ID              int64   `json:"id"`
	Filename        string  `json:"filename"`
	FileSize        *int64  `json:"fileSize"`
	SourceType      string  `json:"sourceType"`
	Status          string  `json:"status"`
	UserID          int64   `json:"userId"`
	Checksum        *string `json:"checksum,omitempty"`
	ImportNotes     *string `json:"importNotes,omitempty"`
	AnalyserVersion *string `json:"analyserVersion,omitempty"`
	// TestRoutineID, when set, marks a transient "test source": we classify it
	// with ONLY this routine (bypassing the active-routine set) instead of the
	// normal pipeline. Lets the WebUI test a single (possibly draft) routine
	// against a real file, including PDF text extraction.
	TestRoutineID *int64 `json:"testRoutineId,omitempty"`
	// TestModule names the daemon that runs this test source. We only handle it
	// when it is "SourceAnalyser" (or empty, for back-compat); test sources for
	// other modules (e.g. "SourceImporter") are ignored here.
	TestModule *string `json:"testModule,omitempty"`
	CreatedAt  string  `json:"createdAt"`
	ModifiedAt string  `json:"modifiedAt"`
}

// IsAnalyserTest reports whether this source is a test source that SourceAnalyser
// should run (its own single-routine analysis test).
func (s *Source) IsAnalyserTest() bool {
	return s.TestRoutineID != nil && (s.TestModule == nil || *s.TestModule == "" || *s.TestModule == "SourceAnalyser")
}

// ListSourcesResponse represents the list sources API response
type ListSourcesResponse struct {
	Sources    []Source `json:"sources"`
	Total      int      `json:"total"`
	MaxSources int      `json:"maxSources"`
}

// UpdateSourceRequest represents the update request payload
type UpdateSourceRequest struct {
	SourceType      string  `json:"sourceType"`
	Status          string  `json:"status"`
	AnalyserVersion *string `json:"analyserVersion,omitempty"`
}

// ListAllSources gets all sources from all users (internal service only).
// There is no dedicated all-users endpoint: the request omits a userId, which
// ResourceManager honours for internal-service callers.
func (c *Client) ListAllSources() ([]Source, error) {
	var listResp ListSourcesResponse
	if err := c.rc.JSON("GET", "/api/sources", nil, &listResp, http.StatusOK); err != nil {
		return nil, err
	}
	return listResp.Sources, nil
}

// UpdateSource updates a source via the ResourceManager API
func (c *Client) UpdateSource(id int64, sourceType, status string, analyserVersion *string) error {
	payload := UpdateSourceRequest{
		SourceType:      sourceType,
		Status:          status,
		AnalyserVersion: analyserVersion,
	}
	return c.rc.JSON("PATCH", fmt.Sprintf("/api/sources/%d", id), payload, nil, http.StatusOK)
}

// DownloadSourceFile downloads the file content for a source.
// At most maxBytes bytes are read from the response body to limit memory usage.
// Returns the file content in memory and the original filename.
func (c *Client) DownloadSourceFile(id int64, maxBytes int64) ([]byte, string, error) {
	resp, err := c.rc.Request("GET", fmt.Sprintf("/api/sources/%d/file", id), nil, http.StatusOK)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()

	// Read at most maxBytes from the response body to avoid unbounded memory usage
	content, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes))
	if err != nil {
		return nil, "", fmt.Errorf("failed to read response body: %w", err)
	}

	// Get the original filename from header
	filename := resp.Header.Get("X-Original-Filename")
	if filename == "" {
		filename = fmt.Sprintf("source_%d", id)
	}

	return content, filename, nil
}
