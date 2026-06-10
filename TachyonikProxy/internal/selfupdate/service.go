// TachyonikProxy
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

package selfupdate

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
)

// ServiceController abstracts the platform-specific commands for
// stop/start/restart/status of the proxy service. Tests inject a fake.
type ServiceController interface {
	Restart(ctx context.Context) error
	Name() string
}

// ResolveServiceController picks the right backend for the running platform
// and install mode. User-space installs do not have a managed service —
// they get a noopServiceController so the apply path can still run, with the
// operator expected to restart manually. The dry-run / status output is
// honest about this.
func ResolveServiceController(mode InstallMode) (ServiceController, error) {
	if mode == ModeUser {
		return &noopServiceController{}, nil
	}
	switch runtime.GOOS {
	case "linux":
		if _, err := exec.LookPath("systemctl"); err != nil {
			return nil, fmt.Errorf("systemctl not found: %w", err)
		}
		return &systemctlController{unit: "tachyonikproxy.service"}, nil
	case "darwin":
		if _, err := exec.LookPath("launchctl"); err != nil {
			return nil, fmt.Errorf("launchctl not found: %w", err)
		}
		return &launchctlController{label: "com.tachyonik.tachyonikproxy"}, nil
	default:
		return nil, fmt.Errorf("unsupported OS for auto-update apply: %s", runtime.GOOS)
	}
}

type systemctlController struct {
	unit string
}

func (c *systemctlController) Name() string { return "systemd:" + c.unit }

func (c *systemctlController) Restart(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, "systemctl", "restart", c.unit)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemctl restart %s: %w (%s)", c.unit, err, out)
	}
	return nil
}

type launchctlController struct {
	label string
}

func (c *launchctlController) Name() string { return "launchd:" + c.label }

func (c *launchctlController) Restart(ctx context.Context) error {
	// launchctl kickstart -k restarts the service if loaded; -k means kill
	// the running instance first.
	cmd := exec.CommandContext(ctx, "launchctl", "kickstart", "-k", "system/"+c.label)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("launchctl kickstart -k %s: %w (%s)", c.label, err, out)
	}
	return nil
}

// noopServiceController is used for user-space installs. The user starts and
// stops the proxy manually; the apply path tells them to restart.
type noopServiceController struct{}

func (c *noopServiceController) Name() string                      { return "manual (user-space)" }
func (c *noopServiceController) Restart(ctx context.Context) error { return nil }
