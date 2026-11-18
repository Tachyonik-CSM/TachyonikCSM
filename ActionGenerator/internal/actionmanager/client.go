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
	"sort"
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
	ModifiedAt  string `json:"modifiedAt"` // ActionManager has no updatedAt
	// ActionRuleID is the rule that raised it; nil for an action no rule
	// raised. A rule's action is identified by its rule and owner (AG-EVAL-09).
	ActionRuleID *int64 `json:"actionRuleId"`
	// ClearedAt is set on a Done action once its rule evaluated to no action
	// for the owner: it no longer holds the rule back (AG-EVAL-26).
	ClearedAt *string `json:"clearedAt"`
}

// Open statuses: the action is still the owner's to deal with. Done is the
// only other.
const (
	StatusNew          = "New"
	StatusAcknowledged = "Acknowledged"
	StatusInProgress   = "InProgress"
	StatusDone         = "Done"
)

// IsOpen reports whether the action is not Done.
func (a Action) IsOpen() bool {
	return a.Status == StatusNew || a.Status == StatusAcknowledged || a.Status == StatusInProgress
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
	// HiddenOptions is the rule's action options that do NOT apply to this
	// user's situation; everything else the rule offers is shown. nil
	// (omitted) means nothing is hidden, which is the answer for a routine
	// that declares no conditions and preserves the behaviour every action had
	// before conditions existed.
	HiddenOptions *[]int64 `json:"hiddenOptions,omitempty"`
}

// RefreshAction brings a rule's open action up to date with the rule's latest
// evaluation: title, type, description, priority and the options that do not
// apply (AG-EVAL-09). Status and assignment stay the user's. Reports whether
// ActionManager changed anything; it writes and announces only then.
func (c *Client) RefreshAction(actionID int64, req CreateActionRequest) (bool, error) {
	payload := map[string]interface{}{
		"title":       req.Title,
		"type":        req.Type,
		"description": req.Description,
		"priority":    req.Priority,
	}
	if req.HiddenOptions != nil {
		payload["hiddenOptions"] = *req.HiddenOptions
	}
	var result struct {
		Changed bool `json:"changed"`
	}
	if err := c.rc.JSON("POST", fmt.Sprintf("/api/internal/actions/%d/refresh", actionID), payload, &result, http.StatusOK); err != nil {
		return false, err
	}
	return result.Changed, nil
}

// ClearDoneActionsByRule marks a rule's Done actions of an owner (0: every
// owner, a shared workspace) as cleared: the rule evaluated to no action, so
// their situation has ended and the next match raises a new action
// (AG-EVAL-26). Returns how many were marked.
func (c *Client) ClearDoneActionsByRule(actionRuleID int64, userID int64) (int, error) {
	payload := map[string]int64{"actionRuleId": actionRuleID, "userId": userID}
	var result struct {
		Cleared int `json:"cleared"`
	}
	if err := c.rc.JSON("POST", "/api/internal/actions/clear-done-by-rule", payload, &result, http.StatusOK); err != nil {
		return 0, err
	}
	return result.Cleared, nil
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

// RuleActions are an owner's actions of one rule, sorted out as AG-EVAL-09
// and -26 need them.
type RuleActions struct {
	// Open is the owner's open actions of the rule, newest first. Normally
	// one; ActionManager refuses a second, but data from before it did may
	// hold more (AG-EVAL-27).
	Open []Action
	// DoneUncleared is whether a Done action of the rule has not been
	// cleared: it holds the rule back.
	DoneUncleared bool
}

// GetRuleActions returns the owner's actions of a rule. The title plays no
// part: an action is the rule's by its rule id alone.
func (c *Client) GetRuleActions(userID int64, actionRuleID int64) (RuleActions, error) {
	actions, err := c.GetActionsForUser(userID)
	if err != nil {
		return RuleActions{}, err
	}
	var ra RuleActions
	for _, a := range actions {
		if a.ActionRuleID == nil || *a.ActionRuleID != actionRuleID || a.UserID != userID {
			continue
		}
		switch {
		case a.IsOpen():
			ra.Open = append(ra.Open, a)
		case a.Status == StatusDone && a.ClearedAt == nil:
			ra.DoneUncleared = true
		}
	}
	// Newest first: ids grow with creation.
	sort.Slice(ra.Open, func(i, j int) bool { return ra.Open[i].ID > ra.Open[j].ID })
	return ra, nil
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
//
// exceptActionID, when not 0, is kept: the open action being updated while
// its older copies are withdrawn (AG-EVAL-27).
func (c *Client) DeleteNewActionsByRule(userID int64, actionRuleID int64, exceptActionID int64) (int, error) {
	payload := map[string]int64{
		"userId":       userID,
		"actionRuleId": actionRuleID,
	}
	if exceptActionID != 0 {
		payload["exceptActionId"] = exceptActionID
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
