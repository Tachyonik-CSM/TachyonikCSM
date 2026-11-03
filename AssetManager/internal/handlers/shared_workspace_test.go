// AssetManager
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests that a shared workspace widens what a request may see without widening
// what it may change: a member reads the workspace's assets, while ownership
// still decides who can modify them.

package handlers

import (
	"testing"

	"tachyonik/assetmanager/internal/config"
)

// sharedWorkspace drives instance-wide host-asset counting: true only in
// appliance mode.
func TestSharedWorkspace(t *testing.T) {
	appliance := &Handler{config: &config.Config{Instance: config.InstanceConfig{Mode: config.ModeAppliance}}}
	if !appliance.sharedWorkspace() {
		t.Error("appliance mode should report a shared workspace")
	}
	saas := &Handler{config: &config.Config{Instance: config.InstanceConfig{Mode: config.ModeSaaS}}}
	if saas.sharedWorkspace() {
		t.Error("saas mode should not report a shared workspace")
	}
}
