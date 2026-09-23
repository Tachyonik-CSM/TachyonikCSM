// ActionGenerator
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package assetmanager is the read-only client for the AssetManager API. It
// supplies the assets and their aggregate statistics that rules reason about.
// This daemon never writes assets.
//
// The request mechanics live in TachyonikLib's restclient: joining the path,
// attaching the service key, checking the status and decoding. What stays here
// is what is specific to this service — its wire types, its paths, and the
// status each call treats as success.
package assetmanager

import (
	"fmt"
	"net/http"
	"time"

	"tachyonik/lib/restclient"
)

// Client handles communication with the AssetManager API.
type Client struct {
	rc *restclient.Client
}

// Asset represents an asset in the system
type Asset struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Type       string `json:"type"`
	Source     string `json:"source"`
	Severity   int    `json:"severity"`
	Score      int    `json:"score"`
	UserID     int64  `json:"userId"`
	CreatedAt  string `json:"createdAt"`
	ModifiedAt string `json:"modifiedAt"`
	LastSeen   string `json:"lastSeen"`
}

// ListResponse represents the response from the assets endpoint
type ListResponse struct {
	Assets []Asset `json:"assets"`
	Total  int     `json:"total"`
}

// AssetStats mirrors AssetManager's /api/assets/stats response.
type AssetStats struct {
	Critical  int `json:"critical"`
	High      int `json:"high"`
	Medium    int `json:"medium"`
	Low       int `json:"low"`
	NoThreat  int `json:"noThreat"`
	Unmanaged int `json:"unmanaged"`
	Total     int `json:"total"`
}

// NewClient creates a new AssetManager API client
func NewClient(baseURL, internalServiceKey string) *Client {
	return &Client{rc: restclient.New(baseURL, internalServiceKey, 10*time.Second)}
}

// GetAssetsForUser retrieves all assets for a specific user
func (c *Client) GetAssetsForUser(userID int64) ([]Asset, error) {
	var result ListResponse
	if err := c.rc.JSON("GET", fmt.Sprintf("/api/assets?userId=%d", userID), nil, &result, http.StatusOK); err != nil {
		return nil, err
	}
	return result.Assets, nil
}

// GetAssetStats retrieves aggregated asset statistics for a specific user.
func (c *Client) GetAssetStats(userID int64) (*AssetStats, error) {
	var stats AssetStats
	if err := c.rc.JSON("GET", fmt.Sprintf("/api/assets/stats?userId=%d", userID), nil, &stats, http.StatusOK); err != nil {
		return nil, err
	}
	return &stats, nil
}
