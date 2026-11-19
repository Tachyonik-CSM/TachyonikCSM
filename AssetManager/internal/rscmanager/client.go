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

// sourceNotFound is ResourceManager's own answer for a source it does not
// have (sources.get, 404 {"error": "Source not found"}).
const sourceNotFound = "Source not found"

// SourceExists reports whether ResourceManager has the source. It answers
// false only on ResourceManager's own "Source not found": a 404 from anything
// else — a wrong path, a proxy in between — is an error, not a deletion. The
// sources' sweep removes the records of a source on false (AM-DATA-46), so a
// misrouted request must never read as "every source is gone".
func (c *Client) SourceExists(id int64) (bool, error) {
	var source Source
	err := c.rest.JSON("GET", fmt.Sprintf("/api/sources/%d", id), nil, &source, http.StatusOK)
	if err == nil {
		return true, nil
	}
	var se *restclient.StatusError
	if errors.As(err, &se) && se.StatusCode == http.StatusNotFound && se.Message == sourceNotFound {
		return false, nil
	}
	return false, err
}
