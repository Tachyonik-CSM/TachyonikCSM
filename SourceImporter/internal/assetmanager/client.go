// SourceImporter
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package assetmanager is the client for the AssetManager API, over which this
// daemon writes everything an import produces: assets, vulnerabilities and
// detections.
//
// It also serves the re-import path, which needs the inverse operation — after a
// source is imported afresh, records belonging to it that the new run did not
// touch are stale and are deleted by modification time.
package assetmanager

import (
	"fmt"
	"net/http"
	"net/url"
	"time"

	"tachyonik/lib/restclient"
)

// Client represents an AssetManager API client. The request plumbing lives in
// tachyonik/lib/restclient; what remains here is the wire types and one line per
// endpoint.
type Client struct {
	rc *restclient.Client
}

// CreateAssetRequest represents the create asset request.
//
// sourceId is the stable ResourceManager source id and sourceLabel the
// filename to show beside it. A legacy `source` string of the form
// "filename (ID: N)" used to travel alongside them, for AssetManager builds
// predating the split; AssetManager parses that form only when no sourceId
// arrives, and this caller always has one — source.ID is a primary key — so
// the fallback was unreachable from here.
type CreateAssetRequest struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	SourceID    int64  `json:"sourceId"`    // ResourceManager source id
	SourceLabel string `json:"sourceLabel"` // denormalised filename
	UserID      int64  `json:"userId,omitempty"`
	LastSeen    string `json:"lastSeen,omitempty"`
}

// Asset represents an asset response
type Asset struct {
	ID           int64     `json:"id"`
	Name         string    `json:"name"`
	Type         string    `json:"type"`
	SourcesLabel string    `json:"sourcesLabel"`
	CreatedAt    time.Time `json:"createdAt"`
	ModifiedAt   time.Time `json:"modifiedAt"`
}

// CreateVulnerabilityRequest represents the create vulnerability request
type CreateVulnerabilityRequest struct {
	Name     string `json:"name"`
	Host     string `json:"host"`
	Port     string `json:"port"`
	Severity int    `json:"severity"`
	Source   string `json:"source"`
	UserID   int64  `json:"userId,omitempty"`
	LastSeen string `json:"lastSeen,omitempty"`
}

// Vulnerability represents a vulnerability response
type Vulnerability struct {
	ID         int64     `json:"id"`
	Name       string    `json:"name"`
	Host       string    `json:"host"`
	Port       string    `json:"port"`
	Severity   int       `json:"severity"`
	UserID     int64     `json:"userId"`
	CreatedAt  time.Time `json:"createdAt"`
	ModifiedAt time.Time `json:"modifiedAt"`
}

// CreateDetectionRequest represents the create detection request
type CreateDetectionRequest struct {
	Name     string `json:"name"`
	Host     string `json:"host"`
	Port     string `json:"port"`
	Source   string `json:"source"`
	UserID   int64  `json:"userId,omitempty"`
	LastSeen string `json:"lastSeen,omitempty"`
}

// Detection represents a detection response
type Detection struct {
	ID         int64     `json:"id"`
	Name       string    `json:"name"`
	Host       string    `json:"host"`
	Port       string    `json:"port"`
	UserID     int64     `json:"userId"`
	CreatedAt  time.Time `json:"createdAt"`
	ModifiedAt time.Time `json:"modifiedAt"`
}

// NewClient creates a new AssetManager API client
func NewClient(baseURL, internalServiceKey string) *Client {
	return &Client{rc: restclient.New(baseURL, internalServiceKey, 10*time.Second)}
}

// DeleteStaleResponse represents the response from delete stale endpoints
type DeleteStaleResponse struct {
	Deleted int    `json:"deleted"`
	Message string `json:"message"`
}

// CreateAsset creates a new asset via the AssetManager API.
func (c *Client) CreateAsset(name, assetType string, sourceID int64, sourceLabel string, userID int64, lastSeen string) (*Asset, error) {
	payload := CreateAssetRequest{
		Name:        name,
		Type:        assetType,
		SourceID:    sourceID,
		SourceLabel: sourceLabel,
		UserID:      userID,
		LastSeen:    lastSeen,
	}
	var asset Asset
	if err := c.rc.JSON("POST", "/api/assets", payload, &asset, http.StatusCreated); err != nil {
		return nil, err
	}
	return &asset, nil
}

// CreateVulnerability creates a new vulnerability via the AssetManager API.
func (c *Client) CreateVulnerability(name, host, port string, severity int, source string, userID int64, lastSeen string) (*Vulnerability, error) {
	payload := CreateVulnerabilityRequest{
		Name:     name,
		Host:     host,
		Port:     port,
		Severity: severity,
		Source:   source,
		UserID:   userID,
		LastSeen: lastSeen,
	}
	var vulnerability Vulnerability
	if err := c.rc.JSON("POST", "/api/vulnerabilities", payload, &vulnerability, http.StatusCreated); err != nil {
		return nil, err
	}
	return &vulnerability, nil
}

// CreateDetection creates a new detection via the AssetManager API.
func (c *Client) CreateDetection(name, host, port, source string, userID int64, lastSeen string) (*Detection, error) {
	payload := CreateDetectionRequest{
		Name:     name,
		Host:     host,
		Port:     port,
		Source:   source,
		UserID:   userID,
		LastSeen: lastSeen,
	}
	var detection Detection
	if err := c.rc.JSON("POST", "/api/detections", payload, &detection, http.StatusCreated); err != nil {
		return nil, err
	}
	return &detection, nil
}

// deleteStale removes records of one kind that belong to a source and have not
// been touched since modifiedBefore — the sweep that follows a re-import, so
// what the previous run created but this one did not produce goes away.
//
// The three exported wrappers below differ only in the collection they name;
// they were three copies of this function, identical but for that one word.
func (c *Client) deleteStale(collection, source string, modifiedBefore time.Time) (int, error) {
	path := fmt.Sprintf("/api/%s/stale?source=%s&modifiedBefore=%s",
		collection, url.QueryEscape(source), url.QueryEscape(modifiedBefore.Format(time.RFC3339)))

	var result DeleteStaleResponse
	if err := c.rc.JSON("DELETE", path, nil, &result, http.StatusOK); err != nil {
		return 0, err
	}
	return result.Deleted, nil
}

// DeleteStaleAssets deletes assets associated with a source that were modified
// before the given timestamp.
func (c *Client) DeleteStaleAssets(source string, modifiedBefore time.Time) (int, error) {
	return c.deleteStale("assets", source, modifiedBefore)
}

// DeleteStaleVulnerabilities deletes vulnerabilities associated with a source
// that were modified before the given timestamp.
func (c *Client) DeleteStaleVulnerabilities(source string, modifiedBefore time.Time) (int, error) {
	return c.deleteStale("vulnerabilities", source, modifiedBefore)
}

// DeleteStaleDetections deletes detections associated with a source that were
// modified before the given timestamp.
func (c *Client) DeleteStaleDetections(source string, modifiedBefore time.Time) (int, error) {
	return c.deleteStale("detections", source, modifiedBefore)
}
