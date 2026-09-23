// ActionGenerator
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package systemmanager is the read-only client for the SystemManager API. It
// supplies the set of users to generate actions for, and per user the
// organisation record, settings and AI assignments the rule context exposes.
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

// Organisation represents a user's organisation.
//
// The whole non-secret record, because all of it reaches the JS rule context:
// a rule about an organisation that has not recorded its homepage, or its
// economic activity, or its address needs to be able to see that those are
// empty. The endpoint has always returned these fields; until now this struct
// discarded everything but the first six, so no rule could be written about
// them however the prompt was worded.
type Organisation struct {
	ID                int64   `json:"id"`
	Name              string  `json:"name"`
	AssetsHosts       int     `json:"assetsHosts"`
	Score             float64 `json:"score"`
	ITStaffHourlyCost int     `json:"itStaffHourlyCost"`
	Currency          string  `json:"currency"`
	// Identity and public presence.
	HomepageURL string `json:"homepageUrl"`
	// Economic activity as a NACE / WZ code at whatever depth is known:
	// "" (unset), "C", "62", "62.0" or "62.01".
	NaceCode string `json:"naceCode"`
	// Employees is this organisation's own headcount; EmployeesAffiliated
	// counts the corporate group and is the figure regulation asks for. Equal
	// for a standalone company, larger for a subsidiary, never smaller.
	Employees           int `json:"employees"`
	EmployeesAffiliated int `json:"employeesAffiliated"`
	// The organisation's own postal address, distinct from the billing
	// identity below.
	AddressStreet     string `json:"addressStreet"`
	AddressPostalCode string `json:"addressPostalCode"`
	AddressCity       string `json:"addressCity"`
	AddressCountry    string `json:"addressCountry"`
	// Billing/VAT identity. Non-secret, and already sent to the browser.
	CompanyLegalName string `json:"companyLegalName"`
	VATNumber        string `json:"vatNumber"`
	VATNumberValid   bool   `json:"vatNumberValid"`
	// The IT service providers the organisation works with. SystemManager sends
	// only what a rule can act on; notes and logos stay behind.
	ITProviders []ITProvider `json:"itProviders"`
	// Questionnaire progress and verdicts, computed by SystemManager from the
	// stored answers when this was fetched. Nil when it could not compute them.
	Questionnaires *Questionnaires `json:"questionnaires"`
}

// Questionnaires is SystemManager's summary of the organisation questionnaires.
type Questionnaires struct {
	NIS2Impact    QuestionnaireImpact    `json:"nis2Impact"`
	NIS2Readiness QuestionnaireReadiness `json:"nis2Readiness"`
	CyberStrategy QuestionnaireStrategy  `json:"cyberStrategy"`
}

// QuestionnaireImpact is the NIS2 Impact self-assessment. Level is "red",
// "orange", "yellow" or "green", and "" until complete.
type QuestionnaireImpact struct {
	Progress            int    `json:"progress"`
	Answered            int    `json:"answered"`
	Total               int    `json:"total"`
	Complete            bool   `json:"complete"`
	Level               string `json:"level"`
	Size                string `json:"size"`
	UnclearTreatedAsYes bool   `json:"unclearTreatedAsYes"`
	LastUpdated         string `json:"lastUpdated"`
	Expired             bool   `json:"expired"`
}

// QuestionnaireReadiness is the NIS2 Readiness questionnaire. Level is "red",
// "yellow" or "green", and "" until complete. ReportingScore is nil while the
// reporting area has no answers.
type QuestionnaireReadiness struct {
	Progress          int      `json:"progress"`
	Answered          int      `json:"answered"`
	Total             int      `json:"total"`
	Complete          bool     `json:"complete"`
	Score             float64  `json:"score"`
	Level             string   `json:"level"`
	UnknownCount      int      `json:"unknownCount"`
	Gaps              []string `json:"gaps"`
	ReportingScore    *float64 `json:"reportingScore"`
	ReportingCritical bool     `json:"reportingCritical"`
	LastUpdated       string   `json:"lastUpdated"`
	Expired           bool     `json:"expired"`
}

// QuestionnaireStrategy is the Cybersecurity Strategy. Complete means every
// required item is present, which is not the same as Progress reaching 100.
type QuestionnaireStrategy struct {
	Progress        int      `json:"progress"`
	Answered        int      `json:"answered"`
	Total           int      `json:"total"`
	Complete        bool     `json:"complete"`
	MissingRequired []string `json:"missingRequired"`
	LastUpdated     string   `json:"lastUpdated"`
	Expired         bool     `json:"expired"`
}

// ITProvider is one IT service provider as SystemManager exposes it to other
// services. Types are keys from a closed set: "cybersecurity",
// "network_infrastructure", "general_it_support".
type ITProvider struct {
	Name         string   `json:"name"`
	HomepageURL  string   `json:"homepageUrl"`
	ContactEmail string   `json:"contactEmail"`
	Types        []string `json:"types"`
}

// UserAISettings is which KIND of provider answers each AI feature:
// "internal" (the operator's), "disabled", "personal" (one of the user's own),
// or "none". Research has no operator fallback, so it is only "personal" or
// "none". Never an id or a name — see SystemManager's user_settings.go.
type UserAISettings struct {
	Research  string `json:"research"`
	Chat      string `json:"chat"`
	Dashboard string `json:"dashboard"`
}

// UserSettings is what the user has configured for themselves, curated by
// SystemManager. Deliberately not the raw preferences blob.
type UserSettings struct {
	AI       UserAISettings `json:"ai"`
	Language string         `json:"language"`
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

// GetOrganisationForUser retrieves the organisation record a user belongs to.
func (c *Client) GetOrganisationForUser(userID int64) (*Organisation, error) {
	var org Organisation
	if err := c.rc.JSON("GET", fmt.Sprintf("/api/internal/organisation?userId=%d", userID), nil, &org, http.StatusOK); err != nil {
		return nil, err
	}
	return &org, nil
}

// GetUserSettings retrieves what the user has configured for themselves.
func (c *Client) GetUserSettings(userID int64) (*UserSettings, error) {
	var settings UserSettings
	if err := c.rc.JSON("GET", fmt.Sprintf("/api/internal/users/%d/settings", userID), nil, &settings, http.StatusOK); err != nil {
		return nil, err
	}
	return &settings, nil
}

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
