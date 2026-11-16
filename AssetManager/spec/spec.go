// AssetManager
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package spec carries AssetManager's API document, openapi.yaml, in the
// binary. AssetManager registers it with AIManager at startup, so the platform
// API catalogue describes the AssetManager that actually runs
// (docs/specs/01-platform-api-catalogue.md, PA-REG-01). The tests here and in
// internal/handlers hold the document to the code (PA-CAT-02, -08).
package spec

import _ "embed"

// OpenAPI is openapi.yaml as it was when the binary was built.
//
//go:embed openapi.yaml
var OpenAPI []byte

// Module is AssetManager's name in the catalogue.
const Module = "assetmanager"
