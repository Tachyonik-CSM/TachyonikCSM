// AssetManager
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package rscmanager is a thin internal client AssetManager uses to resolve the
// source an asset came from, so a record can be reported against the file or
// tool that produced it rather than a bare identifier.
package rscmanager

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"tachyonik/lib/restclient"
)

type Client struct {
	rest *restclient.Client
}

type Source struct {
	ID         int64  `json:"id"`
	Filename   string `json:"filename"`
	FileSize   *int64 `json:"fileSize"`
	SourceType string `json:"sourceType"`
	Status     string `json:"status"`
	UserID     int64  `json:"userId"`
}

func NewClient(baseURL, internalServiceKey string) *Client {
	return &Client{rest: restclient.New(baseURL, internalServiceKey, 10*time.Second)}
}

// GetSource retrieves a source by ID
// Returns nil if source not found (404), error for other failures
func (c *Client) GetSource(id int64) (*Source, error) {
	var source Source
	if err := c.rest.JSON("GET", fmt.Sprintf("/api/src-entries/%d", id), nil, &source, http.StatusOK); err != nil {
		if isNotFound(err) {
			return nil, nil // Source doesn't exist
		}
		return nil, err
	}
	return &source, nil
}

func isNotFound(err error) bool {
	var se *restclient.StatusError
	return errors.As(err, &se) && se.StatusCode == http.StatusNotFound
}
