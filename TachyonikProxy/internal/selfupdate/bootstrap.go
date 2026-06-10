// TachyonikProxy
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

package selfupdate

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// BootstrapResult describes what Bootstrap did, for printing to the operator.
type BootstrapResult struct {
	AlreadyBootstrapped bool
	MovedFromPath       string // path of the original binary, if any
	VersionDir          string // /opt/tachyonik/proxy/<version>
	CurrentSymlink      string
	CLISymlink          string
}

// Bootstrap migrates an existing single-binary install to the
// versioned-symlink layout. It is idempotent: a second run on an already-
// bootstrapped install returns AlreadyBootstrapped == true and changes
// nothing.
//
// Existing layout:
//
//	<CLISymlink>          regular file (the proxy binary)
//
// Resulting layout:
//
//	<VersionRoot>/<currentVersion>/tachyonikproxy   regular file
//	<VersionRoot>/current → <VersionRoot>/<currentVersion>
//	<CLISymlink>          symlink → <VersionRoot>/current/tachyonikproxy
//
// The currentVersion string comes from the running binary's compile-time
// version. We don't try to introspect the file on disk: the binary that
// just ran was either the file we're migrating (so version is its own) or
// already the new layout (so we're a no-op).
func Bootstrap(p *InstallPaths, currentVersion string) (*BootstrapResult, error) {
	if p == nil {
		return nil, errors.New("Bootstrap: nil InstallPaths")
	}
	if currentVersion == "" {
		return nil, errors.New("Bootstrap: empty currentVersion")
	}

	res := &BootstrapResult{
		VersionDir:     p.VersionDir(currentVersion),
		CurrentSymlink: p.CurrentSymlink,
		CLISymlink:     p.CLISymlink,
	}

	if p.LayoutBootstrapped() {
		res.AlreadyBootstrapped = true
		return res, nil
	}

	// Locate the existing binary. We accept either CLISymlink (the
	// canonical place for a system install) or the running executable's
	// path (covers user-space installs where the binary may live in
	// ~/.local/bin already).
	src, err := locateExistingBinary(p)
	if err != nil {
		return nil, err
	}
	res.MovedFromPath = src

	// Create the version directory and place the binary.
	if err := os.MkdirAll(res.VersionDir, 0755); err != nil {
		return nil, fmt.Errorf("create version dir: %w", err)
	}
	dst := p.VersionBinary(currentVersion)
	if err := copyExecutable(src, dst); err != nil {
		return nil, fmt.Errorf("copy binary into version dir: %w", err)
	}

	// Create the current symlink (atomic via temp + rename).
	if err := writeSymlinkAtomic(res.VersionDir, p.CurrentSymlink); err != nil {
		return nil, fmt.Errorf("create current symlink: %w", err)
	}

	// Replace the CLI binary with a symlink into the chain. We do this
	// last so that a crash before this point leaves the system bootable
	// from the original CLI path.
	cliTarget := filepath.Join(p.CurrentSymlink, "tachyonikproxy")
	if err := writeSymlinkAtomic(cliTarget, p.CLISymlink); err != nil {
		return nil, fmt.Errorf("replace CLI symlink: %w", err)
	}

	// Make sure the staging dir exists so the first apply doesn't have to.
	if err := os.MkdirAll(p.StagingRoot, 0755); err != nil {
		return nil, fmt.Errorf("create staging root: %w", err)
	}

	// Write an initial state file so future runs know what's active.
	st := &State{
		Active: currentVersion,
		Phase:  PhaseIdle,
	}
	if err := SaveState(p.StateFile, st); err != nil {
		return nil, fmt.Errorf("write initial state: %w", err)
	}

	return res, nil
}

// locateExistingBinary returns the path to the existing proxy binary, in
// preference order: CLISymlink (if it's a regular file), running
// executable (resolved through any symlinks).
func locateExistingBinary(p *InstallPaths) (string, error) {
	if info, err := os.Lstat(p.CLISymlink); err == nil {
		if info.Mode()&os.ModeSymlink == 0 && info.Mode().IsRegular() {
			return p.CLISymlink, nil
		}
	}
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("locate own executable: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(exe)
	if err != nil {
		resolved = exe
	}
	if info, err := os.Stat(resolved); err == nil && info.Mode().IsRegular() {
		return resolved, nil
	}
	return "", fmt.Errorf("could not locate an existing proxy binary to migrate (looked at %s and %s)", p.CLISymlink, exe)
}

func copyExecutable(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(dst)
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		os.Remove(dst)
		return err
	}
	return out.Close()
}

// writeSymlinkAtomic writes a symlink at linkPath pointing to target. If
// linkPath already exists (regular file or symlink), it is replaced
// atomically via a temp symlink and rename.
func writeSymlinkAtomic(target, linkPath string) error {
	if err := os.MkdirAll(filepath.Dir(linkPath), 0755); err != nil {
		return err
	}
	tmp := linkPath + ".swap"
	_ = os.Remove(tmp)
	if err := os.Symlink(target, tmp); err != nil {
		return err
	}
	if err := os.Rename(tmp, linkPath); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
