// SourceImporter
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package rscmanager is the client for the ResourceManager API, the source of
// the files this daemon imports. It lists sources across all users (an internal
// service call), downloads a source's bytes, and writes back the type, status,
// import notes and importer version that record what happened to it.
package rscmanager

import (
	"fmt"
	"io"
	"net/http"
	"time"

	"tachyonik/lib/restclient"
)

// MaxSourceFileBytes bounds a downloaded source file.
//
// It exists so the daemon's memory does not depend on another service's
// configuration staying correct, and the number is chosen against what parsing
// a file costs rather than against the file itself: building a DOM amplifies,
// measurably ~48x for XML and ~20x for JSON, and that happens in Go before any
// routine runs — outside the execution budget, which interrupts JavaScript and
// not its host. So the read cap is the only thing standing between an uploaded
// file and the heap it turns into.
//
// 16 MiB is roughly three times the largest upload tier ResourceManager grants
// today (5 MB registered, 3 MB anonymous, see rolelimits), leaving room for a
// per-user plan to raise its own limit without this rejecting the result, while
// keeping the worst case a container memory limit has to absorb in the hundreds
// of megabytes rather than the gigabytes 64 MiB allowed.
const MaxSourceFileBytes = 16 << 20 // 16 MiB

// Client represents a ResourceManager API client. The request plumbing lives in
// tachyonik/lib/restclient; what remains here is the wire types and one line per
// endpoint.
type Client struct {
	rc *restclient.Client
}

// UpdateSourceRequest represents the update source request
type UpdateSourceRequest struct {
	SourceType      string  `json:"sourceType"`
	Status          string  `json:"status"`
	ImportNotes     *string `json:"importNotes,omitempty"`
	ImporterVersion *string `json:"importerVersion,omitempty"`
}

// Source represents a source response
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
	ImporterVersion *string `json:"importerVersion,omitempty"`
	// TestRoutineID marks a transient routine-test source. TestModule names the
	// daemon that runs the test: normal imports never touch a test source; only
	// a "SourceImporter" test source is dry-run by this daemon (no data written).
	TestRoutineID *int64    `json:"testRoutineId,omitempty"`
	TestModule    *string   `json:"testModule,omitempty"`
	CreatedAt     time.Time `json:"createdAt"`
	ModifiedAt    time.Time `json:"modifiedAt"`
}

// IsImporterTest reports whether this source is a test source that SourceImporter
// should dry-run (its own single-routine import test).
func (s *Source) IsImporterTest() bool {
	return s.TestRoutineID != nil && s.TestModule != nil && *s.TestModule == "SourceImporter"
}

// ListSourcesResponse represents the list sources API response
type ListSourcesResponse struct {
	Sources    []Source `json:"sources"`
	Total      int      `json:"total"`
	MaxSources int      `json:"maxSources"`
}

// NewClient creates a new ResourceManager API client
func NewClient(baseURL, internalServiceKey string) *Client {
	return &Client{rc: restclient.New(baseURL, internalServiceKey, 10*time.Second)}
}

// ListAllSources gets all sources from all users (internal service only)
func (c *Client) ListAllSources() ([]Source, error) {
	var listResp ListSourcesResponse
	if err := c.rc.JSON("GET", "/api/sources", nil, &listResp, http.StatusOK); err != nil {
		return nil, err
	}
	return listResp.Sources, nil
}

// UpdateSource updates an source's type and status via the ResourceManager API
func (c *Client) UpdateSource(id int64, sourceType, status string, importNotes *string, importerVersion *string) (*Source, error) {
	payload := UpdateSourceRequest{
		SourceType:      sourceType,
		Status:          status,
		ImportNotes:     importNotes,
		ImporterVersion: importerVersion,
	}
	var source Source
	path := fmt.Sprintf("/api/sources/%d", id)
	if err := c.rc.JSON("PATCH", path, payload, &source, http.StatusOK); err != nil {
		return nil, err
	}
	return &source, nil
}

// DownloadSourceFile downloads the file content for a source
// Returns the file content in memory and the original filename
func (c *Client) DownloadSourceFile(id int64) ([]byte, string, error) {
	// Request rather than JSON: this endpoint returns file bytes and carries
	// the original name in a header, so the caller owns the body.
	resp, err := c.rc.Request("GET", fmt.Sprintf("/api/sources/%d/file", id), nil, http.StatusOK)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()

	// Bounded, rather than trusting the upload limit another service enforces.
	// The body is a user-supplied file; reading it whole is the point, but an
	// unbounded ReadAll makes the daemon's memory a function of what someone
	// managed to get past ResourceManager. One byte past the cap distinguishes
	// "exactly at the limit" from "truncated".
	content, err := io.ReadAll(io.LimitReader(resp.Body, MaxSourceFileBytes+1))
	if err != nil {
		return nil, "", fmt.Errorf("failed to read response body: %w", err)
	}
	if int64(len(content)) > MaxSourceFileBytes {
		return nil, "", fmt.Errorf("source %d exceeds the %d MiB import limit", id, MaxSourceFileBytes>>20)
	}

	filename := resp.Header.Get("X-Original-Filename")
	if filename == "" {
		filename = fmt.Sprintf("source_%d", id)
	}
	return content, filename, nil
}
