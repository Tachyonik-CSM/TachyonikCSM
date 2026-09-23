// ActionGenerator
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package actionmanager is the client for the ActionManager API, where the
// actions this daemon produces are created.
//
// The request mechanics live in TachyonikLib's restclient; what stays here is
// this service's wire types, its paths, and the status each call treats as
// success — note that creating an action answers 201, not 200.
//
// It also serves the withdrawal path: when a rule stops triggering, its
// outstanding "New" actions are deleted, so an action disappears once the user
// has done what it asked rather than lingering as stale advice.
package actionmanager

import (
	"fmt"
	"net/http"
	"time"

	"tachyonik/lib/restclient"
)

// Client handles communication with the ActionManager API.
type Client struct {
	rc *restclient.Client
}

// Action represents an action in the system
type Action struct {
	ID          int64  `json:"id"`
	UserID      int64  `json:"userId"`
	Title       string `json:"title"`
	Type        string `json:"type"`
	Description string `json:"description"`
	Status      string `json:"status"`
	Priority    int    `json:"priority"`
	AssignedTo  string `json:"assignedTo"` // "User", "Support", "ActionExecutor"
	IssuedBy    string `json:"issuedBy"`   // "User", "Support", "ActionGenerator"
	Trigger     string `json:"trigger"`    // Trigger string
	CreatedAt   string `json:"createdAt"`
	UpdatedAt   string `json:"updatedAt"`
}

// CreateActionRequest represents the request to create an action
type CreateActionRequest struct {
	UserID       int64  `json:"userId"`
	Title        string `json:"title"`
	Type         string `json:"type"`
	Description  string `json:"description"`
	Status       string `json:"status"`
	Priority     int    `json:"priority"`
	AssignedTo   string `json:"assignedTo"`             // "User", "Support", "ActionExecutor"
	IssuedBy     string `json:"issuedBy"`               // "User", "Support", "ActionGenerator"
	Trigger      string `json:"trigger"`                // Trigger string
	ActionRuleID *int64 `json:"actionRuleId,omitempty"` // FK to the action rule that created this
}

// NewClient creates a new ActionManager API client
func NewClient(baseURL, internalServiceKey string) *Client {
	return &Client{rc: restclient.New(baseURL, internalServiceKey, 10*time.Second)}
}

// GetActionsForUser retrieves all actions for a specific user
func (c *Client) GetActionsForUser(userID int64) ([]Action, error) {
	var result struct {
		Actions []Action `json:"actions"`
		Total   int      `json:"total"`
	}
	if err := c.rc.JSON("GET", fmt.Sprintf("/api/actions?userId=%d", userID), nil, &result, http.StatusOK); err != nil {
		return nil, err
	}
	return result.Actions, nil
}

// CreateAction creates a new action via the ActionManager API.
func (c *Client) CreateAction(req CreateActionRequest) (*Action, error) {
	var action Action
	if err := c.rc.JSON("POST", "/api/actions", req, &action, http.StatusCreated); err != nil {
		return nil, err
	}
	return &action, nil
}

// ActionExists reports whether the user already has an action with this title.
// A filter over the list call rather than an endpoint of its own.
func (c *Client) ActionExists(userID int64, title string) (bool, error) {
	actions, err := c.GetActionsForUser(userID)
	if err != nil {
		return false, err
	}

	for _, action := range actions {
		if action.Title == title {
			return true, nil
		}
	}

	return false, nil
}

// deleteResponse is what both withdrawal endpoints answer with.
type deleteResponse struct {
	Deleted    int     `json:"deleted"`
	DeletedIDs []int64 `json:"deletedIds"`
}

// DeleteNewActionsByRule asks ActionManager to remove any New-status action
// that a given rule generated for a given user. Used by the event-watch
// iteration to garbage-collect suggestions the user hasn't touched once the
// triggering rule stops matching the context.
// Returns the number of actions actually deleted.
func (c *Client) DeleteNewActionsByRule(userID int64, actionRuleID int64) (int, error) {
	payload := map[string]int64{
		"userId":       userID,
		"actionRuleId": actionRuleID,
	}
	var result deleteResponse
	if err := c.rc.JSON("POST", "/api/internal/actions/delete-by-rule", payload, &result, http.StatusOK); err != nil {
		return 0, err
	}
	return result.Deleted, nil
}

// DeleteNewActionsByRuleExceptUser removes a rule's New-status actions from
// every user except the one named. The shared-workspace counterpart of the
// call above: one action is kept on the primary owner and the copies other
// members accumulated are withdrawn.
// Returns the number of actions actually deleted.
func (c *Client) DeleteNewActionsByRuleExceptUser(actionRuleID int64, keepUserID int64) (int, error) {
	payload := map[string]int64{
		"actionRuleId": actionRuleID,
		"keepUserId":   keepUserID,
	}
	var result deleteResponse
	if err := c.rc.JSON("POST", "/api/internal/actions/delete-by-rule-shared", payload, &result, http.StatusOK); err != nil {
		return 0, err
	}
	return result.Deleted, nil
}
