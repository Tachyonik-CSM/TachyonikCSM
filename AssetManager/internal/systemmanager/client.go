// AssetManager
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package systemmanager is the client for the SystemManager API. It supplies
// the users' limits and the workspace membership that scope a request, and the
// organisation record whose declared estate the Coverage Score is measured
// against; this service sends its sub-scores, statistics entries and audit
// events there.
package systemmanager

import (
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"tachyonik/lib/restclient"
	libsm "tachyonik/lib/systemmanager"
)

type Client struct {
	*libsm.Client // shared core: CreateAuditEvent

	rest *restclient.Client

	// statThrottleMu protects statLastEmit. We rate-limit each (key) to one
	// outbound POST per statThrottleWindow so a 100-asset import doesn't
	// produce 100 HTTP calls — server-side coalesce would still collapse
	// them, but this keeps the wire quiet too.
	statThrottleMu     sync.Mutex
	statLastEmit       map[string]time.Time
	statThrottleWindow time.Duration
}

type User struct {
	ID                 int64  `json:"id"`
	Username           string `json:"username"`
	Email              string `json:"email"`
	Role               string `json:"role"`
	MaxSources         int    `json:"maxSources"`
	MaxFileSize        int    `json:"maxFileSize"`
	MaxHostAssets      int    `json:"maxHostAssets"`
	MaxActions         int    `json:"maxActions"`
	MaxVulnerabilities int    `json:"maxVulnerabilities"`
	MaxDetections      int    `json:"maxDetections"`
	// Active is false for an account an admin deactivated. A pointer, so a
	// record from a SystemManager that predates the field reads as unknown
	// rather than as deactivated.
	Active *bool `json:"active,omitempty"`
}

func NewClient(baseURL, internalServiceKey string) *Client {
	return &Client{
		Client:             libsm.NewClient(baseURL, internalServiceKey),
		rest:               restclient.New(baseURL, internalServiceKey, 10*time.Second),
		statLastEmit:       make(map[string]time.Time),
		statThrottleWindow: 1 * time.Second,
	}
}

// Organisation represents an organisation from SystemManager
type Organisation struct {
	ID          int64   `json:"id"`
	Name        string  `json:"name"`
	AssetsHosts int     `json:"assetsHosts"`
	Score       float64 `json:"score"`
}

// GetOrganisationByUserID retrieves the organisation for a user
func (c *Client) GetOrganisationByUserID(userID int64) (*Organisation, error) {
	var org Organisation
	if err := c.rest.JSON("GET", fmt.Sprintf("/api/internal/organisation?userId=%d", userID), nil, &org, http.StatusOK); err != nil {
		if isNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return &org, nil
}

// UpdateOrganisationSubScore writes one sub-score (exposure, coverage, or
// capability) for a user's organisation. SystemManager recomputes the overall
// score atomically from the three stored sub-scores.
func (c *Client) UpdateOrganisationSubScore(userID int64, subScore string, value float64) error {
	body := map[string]interface{}{"userId": userID, "subScore": subScore, "value": value}
	return c.rest.JSON("PATCH", "/api/internal/organisation/sub-score", body, nil, http.StatusOK)
}

// ListAllUserIDs returns the IDs of every user known to SystemManager. Used by
// startup recalc and global fan-out triggers.
func (c *Client) ListAllUserIDs() ([]int64, error) {
	var result struct {
		UserIDs []int64 `json:"userIds"`
	}
	if err := c.rest.JSON("GET", "/api/internal/users/ids", nil, &result, http.StatusOK); err != nil {
		return nil, err
	}
	return result.UserIDs, nil
}

// GetUser retrieves a user by ID via SystemManager's internal endpoint.
// Returns nil if user not found (404), error for other failures.
//
// The path used to be `/api/users/{id}`, which is not a registered route on
// SystemManager — every call 404'd and the caller silently skipped its
// host-asset limit check. The correct internal-key route is
// `/api/internal/users/{id}` (handlers.go: GetUserInternal).
func (c *Client) GetUser(id int64) (*User, error) {
	var user User
	if err := c.rest.JSON("GET", fmt.Sprintf("/api/internal/users/%d", id), nil, &user, http.StatusOK); err != nil {
		if isNotFound(err) {
			return nil, nil // User doesn't exist
		}
		return nil, err
	}
	return &user, nil
}

// GetGroupMemberIDs returns all user IDs that share a group with the given user.
// If the user is not in a group, returns a slice containing only their own ID.
func (c *Client) GetGroupMemberIDs(userID int64) ([]int64, error) {
	var result struct {
		UserIDs []int64 `json:"userIds"`
	}
	if err := c.rest.JSON("GET", fmt.Sprintf("/api/internal/group-members?userId=%d", userID), nil, &result, http.StatusOK); err != nil {
		return []int64{userID}, err
	}
	if len(result.UserIDs) == 0 {
		return []int64{userID}, nil
	}
	return result.UserIDs, nil
}

// CreateStatsEntry posts a timeline data point (userId, key, value) to
// SystemManager. Best-effort. The client throttles per-key so a burst of
// updates on the same key only crosses the wire once per second; the
// server still collapses on its own window. Returns nil if throttled.
func (c *Client) CreateStatsEntry(userID int64, key string, value float64) error {
	c.statThrottleMu.Lock()
	throttleKey := fmt.Sprintf("%d:%s", userID, key)
	if last, ok := c.statLastEmit[throttleKey]; ok && time.Since(last) < c.statThrottleWindow {
		c.statThrottleMu.Unlock()
		return nil
	}
	c.statLastEmit[throttleKey] = time.Now()
	c.statThrottleMu.Unlock()

	body := map[string]interface{}{"userId": userID, "key": key, "value": value}
	// SystemManager answers 201; 200 is taken as well, as any success was.
	if err := c.rest.JSON("POST", "/api/internal/stats", body, nil, http.StatusCreated, http.StatusOK); err != nil {
		return fmt.Errorf("stats entry: %w", err)
	}
	return nil
}

func isNotFound(err error) bool {
	var se *restclient.StatusError
	return errors.As(err, &se) && se.StatusCode == http.StatusNotFound
}
