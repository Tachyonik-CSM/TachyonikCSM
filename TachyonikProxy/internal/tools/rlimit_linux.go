// TachyonikProxy
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build linux

package tools

import (
	"errors"
	"fmt"
	"os/exec"
	"syscall"

	"golang.org/x/sys/unix"

	"tachyonik/tachyonikproxy/internal/config"
	"tachyonik/tachyonikproxy/internal/logger"
)

// applyResourceLimits enforces tool.MaxCPUSeconds / MaxMemoryMB by calling
// prlimit(2) against the child PID after cmd.Start() has returned. The
// limit applies to the kernel task struct and persists across execve, so
// it is correct whether the child is still in Go's start-proc or has
// already invoked the target binary.
//
// We deliberately do NOT use a shell wrapper. The previous implementation
// of this function did, which silently re-introduced shell quoting
// concerns on tools where allowed_chars was permissive or absent.
func applyResourceLimits(cmd *exec.Cmd, tool *config.ToolConfig) error {
	if cmd.Process == nil {
		return errors.New("applyResourceLimits: cmd not started")
	}
	pid := cmd.Process.Pid

	if tool.MaxCPUSeconds > 0 {
		setRlimit(pid, unix.RLIMIT_CPU, uint64(tool.MaxCPUSeconds), tool.Name, fmt.Sprintf("RLIMIT_CPU=%ds", tool.MaxCPUSeconds))
	}
	if tool.MaxMemoryMB > 0 {
		setRlimit(pid, unix.RLIMIT_AS, uint64(tool.MaxMemoryMB)*1024*1024, tool.Name, fmt.Sprintf("RLIMIT_AS=%dMB", tool.MaxMemoryMB))
	}
	if tool.MaxProcesses > 0 {
		setRlimit(pid, unix.RLIMIT_NPROC, uint64(tool.MaxProcesses), tool.Name, fmt.Sprintf("RLIMIT_NPROC=%d", tool.MaxProcesses))
	}
	if tool.MaxFileSizeMB > 0 {
		setRlimit(pid, unix.RLIMIT_FSIZE, uint64(tool.MaxFileSizeMB)*1024*1024, tool.Name, fmt.Sprintf("RLIMIT_FSIZE=%dMB", tool.MaxFileSizeMB))
	}

	return nil
}

// setRlimit applies one prlimit(2) (Cur==Max==value) to pid. ESRCH means the
// child already exited (racing exit, not a real error); anything else is
// best-effort and logged. desc is the human label for the warning, e.g.
// "RLIMIT_AS=64MB".
func setRlimit(pid, resource int, value uint64, toolName, desc string) {
	l := unix.Rlimit{Cur: value, Max: value}
	if err := unix.Prlimit(pid, resource, &l, nil); err != nil && !errors.Is(err, unix.ESRCH) {
		logger.Warnf("Tool %q: failed to set %s: %v", toolName, desc, err)
	}
}

// setProcessGroup places the child in its own process group so the whole
// group (the tool plus anything it spawns) can be signalled at once on
// timeout. Without it, context cancellation kills only the direct child and
// leaves grandchildren running.
func setProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// killProcessGroup SIGKILLs the child's entire process group. Wired as the
// command's Cancel hook so a timed-out tool cannot leave detached children
// behind. The child is the group leader (pgid == pid) thanks to Setpgid.
func killProcessGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return unix.Kill(-cmd.Process.Pid, unix.SIGKILL)
}
