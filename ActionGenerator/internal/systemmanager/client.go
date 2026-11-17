// ActionGenerator
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package systemmanager is the read-only client for the SystemManager API. It
// supplies the set of users to generate actions for. What a routine reads
// about a user's organisation and settings it reads through the API bridge,
// not through this client.
//
// It also reports the workspace mode, which decides whether rules are evaluated
// once per user or once against the whole shared workspace.
//
// The request mechanics live in TachyonikLib's restclient; what stays here is
// this service's wire types and paths.
package systemmanager

import (
	"fmt"
	"net/http"
	"time"

	"tachyonik/lib/restclient"
)

// Client handles communication with the SystemManager API.
type Client struct {
	rc *restclient.Client
}

// User represents a user in the system
type User struct {
	ID            int64  `json:"id"`
	Username      string `json:"username"`
	Email         string `json:"email"`
	Role          string `json:"role"`
	MaxHostAssets int    `json:"maxHostAssets"`
	CreatedAt     string `json:"createdAt"`
}

// NewClient creates a new SystemManager API client
func NewClient(baseURL, internalServiceKey string) *Client {
	return &Client{rc: restclient.New(baseURL, internalServiceKey, 10*time.Second)}
}

// GetAllUsers retrieves every user. Answers with a bare array, unlike most of
// the platform's list endpoints.
func (c *Client) GetAllUsers() ([]User, error) {
	var users []User
	if err := c.rc.JSON("GET", "/api/internal/users", nil, &users, http.StatusOK); err != nil {
		return nil, err
	}
	return users, nil
}

// GetUser retrieves a specific user by ID
func (c *Client) GetUser(userID int64) (*User, error) {
	var user User
	if err := c.rc.JSON("GET", fmt.Sprintf("/api/internal/users/%d", userID), nil, &user, http.StatusOK); err != nil {
		return nil, err
	}
	return &user, nil
}

// Workspace is the instance's operating mode and its primary owner.
type Workspace struct {
	InstanceMode  string `json:"instanceMode"`
	PrimaryUserID int64  `json:"primaryUserId"`
}

// IsAppliance reports whether the instance runs as a single shared workspace.
func (w Workspace) IsAppliance() bool { return w.InstanceMode == "appliance" }

// GetWorkspace fetches the instance operating mode + primary user so the
// generator can decide between per-user and shared-workspace generation.
func (c *Client) GetWorkspace() (*Workspace, error) {
	var ws Workspace
	if err := c.rc.JSON("GET", "/api/internal/workspace", nil, &ws, http.StatusOK); err != nil {
		return nil, err
	}
	return &ws, nil
}
