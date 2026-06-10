// TachyonikProxy
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !linux

package tools

import (
	"os/exec"
	"sync"

	"tachyonik/tachyonikproxy/internal/config"
	"tachyonik/tachyonikproxy/internal/logger"
)

var rlimitWarnedOnce sync.Once

// applyResourceLimits is a no-op on non-Linux platforms. macOS does not
// expose prlimit(2), and using setrlimit(2) on the parent before
// cmd.Start() would also throttle the proxy daemon itself. A correct
// per-child implementation would require a fork-and-setrlimit-before-exec
// helper which is not in scope.
//
// Tools with MaxCPUSeconds / MaxMemoryMB configured will get a one-time
// warning logged so the operator knows the limits aren't being enforced.
func applyResourceLimits(cmd *exec.Cmd, tool *config.ToolConfig) error {
	if tool.MaxCPUSeconds > 0 || tool.MaxMemoryMB > 0 {
		rlimitWarnedOnce.Do(func() {
			logger.Warnf("Per-tool resource limits (max_cpu_seconds, max_memory_mb) are Linux-only on this build. Configured limits will not be enforced.")
		})
	}
	return nil
}

// setProcessGroup is a no-op on non-Linux: process-group semantics differ and
// the default context Cancel still kills the direct child on timeout.
func setProcessGroup(cmd *exec.Cmd) {}

// killProcessGroup falls back to killing just the direct child.
func killProcessGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}
