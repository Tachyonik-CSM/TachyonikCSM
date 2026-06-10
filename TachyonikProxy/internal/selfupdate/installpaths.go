// TachyonikProxy
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

package selfupdate

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// InstallMode is either "system" (root-owned, /opt + /usr/local/bin) or
// "user" (under $HOME). Determined from the running binary's location, not
// from a configuration knob — operators don't have to keep config in sync
// with where they put the binary.
type InstallMode string

const (
	ModeSystem InstallMode = "system"
	ModeUser   InstallMode = "user"
)

// InstallPaths is everything the apply path needs to know about where files
// live for a given install mode. A single source of truth so every helper
// agrees on the layout.
type InstallPaths struct {
	Mode InstallMode

	// VersionRoot is the parent under which per-version directories live,
	// e.g. /opt/tachyonik/proxy or ~/.local/share/tachyonik/proxy. Each
	// version directory contains exactly one file: tachyonikproxy.
	VersionRoot string

	// CurrentSymlink is VersionRoot/current — the atomic-swap target.
	CurrentSymlink string

	// CLISymlink is the user-facing path that resolves through the
	// versioned-symlink chain to the active binary. Existing systemd units
	// and shell users invoke the proxy through this path.
	CLISymlink string

	// StateFile holds update-state.json.
	StateFile string

	// StagingRoot is a scratch directory under VersionRoot used to extract
	// the artifact before it is renamed into its final version dir. Living
	// on the same filesystem as VersionRoot guarantees that os.Rename is
	// atomic.
	StagingRoot string
}

// ResolveInstallPaths picks the right layout for the running binary.
//
// Detection rule: if the executable path lives under the user's home
// directory we treat the install as user-space; otherwise system-wide.
// This matches what install-tachyonikproxy.sh produces.
func ResolveInstallPaths() (*InstallPaths, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("locate own executable: %w", err)
	}
	exeAbs, err := filepath.EvalSymlinks(exe)
	if err != nil {
		// EvalSymlinks fails on Windows for some service paths and on
		// inaccessible chains; fall back to the raw path so the caller
		// gets *something* useful in error messages.
		exeAbs = exe
	}

	home, _ := os.UserHomeDir()
	userInstall := home != "" && hasPrefixSep(exeAbs, home)

	if userInstall {
		base := filepath.Join(home, ".local", "share", "tachyonik", "proxy")
		return &InstallPaths{
			Mode:           ModeUser,
			VersionRoot:    base,
			CurrentSymlink: filepath.Join(base, "current"),
			CLISymlink:     filepath.Join(home, ".local", "bin", "tachyonikproxy"),
			StateFile:      filepath.Join(base, "update-state.json"),
			StagingRoot:    filepath.Join(base, ".staging"),
		}, nil
	}

	switch runtime.GOOS {
	case "windows":
		return nil, fmt.Errorf("Phase 2 auto-update apply is not supported on Windows yet")
	case "linux", "darwin":
		base := "/opt/tachyonik/proxy"
		return &InstallPaths{
			Mode:           ModeSystem,
			VersionRoot:    base,
			CurrentSymlink: filepath.Join(base, "current"),
			CLISymlink:     "/usr/local/bin/tachyonikproxy",
			StateFile:      "/var/lib/tachyonik/proxy/update-state.json",
			StagingRoot:    filepath.Join(base, ".staging"),
		}, nil
	default:
		return nil, fmt.Errorf("unsupported platform %s/%s for auto-update apply", runtime.GOOS, runtime.GOARCH)
	}
}

// VersionDir returns the directory that holds the tachyonikproxy binary for
// the given version, e.g. /opt/tachyonik/proxy/1.4.0.
func (p *InstallPaths) VersionDir(version string) string {
	return filepath.Join(p.VersionRoot, version)
}

// VersionBinary returns the path to the binary inside a version directory.
func (p *InstallPaths) VersionBinary(version string) string {
	return filepath.Join(p.VersionDir(version), "tachyonikproxy")
}

// CurrentVersionFromSymlink reads VersionRoot/current and returns the
// version name (the directory's basename). Returns an empty string and a
// non-nil error if the symlink does not exist or does not resolve to a
// child of VersionRoot.
func (p *InstallPaths) CurrentVersionFromSymlink() (string, error) {
	target, err := os.Readlink(p.CurrentSymlink)
	if err != nil {
		return "", err
	}
	// The symlink may be relative; resolve it against the symlink's parent.
	if !filepath.IsAbs(target) {
		target = filepath.Join(filepath.Dir(p.CurrentSymlink), target)
	}
	target = filepath.Clean(target)
	if filepath.Dir(target) != filepath.Clean(p.VersionRoot) {
		return "", fmt.Errorf("current symlink %q points outside version root %q",
			target, p.VersionRoot)
	}
	return filepath.Base(target), nil
}

// LayoutBootstrapped reports whether the new versioned-symlink layout is in
// place: VersionRoot exists and current is a symlink that resolves to a
// version directory containing a binary.
func (p *InstallPaths) LayoutBootstrapped() bool {
	v, err := p.CurrentVersionFromSymlink()
	if err != nil {
		return false
	}
	_, err = os.Stat(p.VersionBinary(v))
	return err == nil
}

// hasPrefixSep returns true iff path is the prefix or a sub-path of prefix.
// Avoids the classic bug where /home/long matches /home/longer.
func hasPrefixSep(path, prefix string) bool {
	if path == prefix {
		return true
	}
	if len(path) <= len(prefix) {
		return false
	}
	return path[:len(prefix)] == prefix && path[len(prefix)] == filepath.Separator
}
