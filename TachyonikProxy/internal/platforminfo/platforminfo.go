// TachyonikProxy
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package platforminfo tells the platform where and how this proxy is
// installed: the operating system, the architecture, and the installation
// method. The proxy reports it in the MCP initialize reply
// (tachyonik.platform), ToolManager forwards it to ResourceManager, and the
// WebUI's Upgrade dialog then gives the one update path that fits, instead of
// every path there is.
//
// The installation method is read from what each installer leaves behind,
// never from configuration:
//
//   - deb / rpm: the marker the packages own (selfupdate.PackageManagedMarker);
//     dpkg's record of the package tells a .deb from an .rpm.
//   - pkg: the macOS installer package's receipt.
//   - msi: the binary in the Windows installer's directory under Program Files.
//   - user: a binary under the user's home directory (install-tachyonikproxy.sh
//     without sudo).
//   - tarball: a system install on the versioned layout self-update keeps
//     (/opt/tachyonik/proxy/<version>/), which updates itself.
//   - tarball-legacy: a system install from before that layout, which needs
//     `self-update --bootstrap-layout` once.
//   - unknown: anything else, a build run from a source tree among them.
//
// Detection only reads files and runs read-only version commands; anything it
// cannot read is reported as "" or unknown, never as an error.
package platforminfo

import (
	"bufio"
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"tachyonik/tachyonikproxy/internal/selfupdate"
)

// Installation methods.
const (
	MethodDeb           = "deb"
	MethodRPM           = "rpm"
	MethodPkg           = "pkg"
	MethodMSI           = "msi"
	MethodUser          = "user"
	MethodTarball       = "tarball"
	MethodTarballLegacy = "tarball-legacy"
	MethodUnknown       = "unknown"
)

// Paths the installers leave behind.
const (
	dpkgRecord      = "/var/lib/dpkg/info/tachyonikproxy.list"
	macReceipt      = "/var/db/receipts/com.tachyonik.tachyonikproxy.plist"
	systemVersions  = "/opt/tachyonik/proxy"
	systemCLI       = "/usr/local/bin/tachyonikproxy"
	osRelease       = "/etc/os-release"
	windowsInstall  = `Tachyonik\TachyonikProxy`
	commandDeadline = 3 * time.Second
)

// Platform is what the proxy reports about itself.
type Platform struct {
	OS            string `json:"os"`            // runtime.GOOS: linux, darwin, windows
	Arch          string `json:"arch"`          // runtime.GOARCH: amd64, arm64
	OSName        string `json:"osName"`        // e.g. "Debian GNU/Linux 13 (trixie)"; "" when unreadable
	InstallMethod string `json:"installMethod"` // one of the Method* constants
	// AutoUpdate is whether this installation updates itself: an install that
	// can (tarball) with auto_update.enabled in its configuration.
	AutoUpdate bool `json:"autoUpdate"`
}

// env is everything detection reads, so tests can supply their own.
type env struct {
	goos, goarch string
	executable   func() (string, error)
	evalSymlinks func(string) (string, error)
	home         func() (string, error)
	getenv       func(string) string
	exists       func(string) bool
	readFile     func(string) ([]byte, error)
	run          func(name string, args ...string) (string, error)
}

func realEnv() env {
	return env{
		goos:         runtime.GOOS,
		goarch:       runtime.GOARCH,
		executable:   os.Executable,
		evalSymlinks: filepath.EvalSymlinks,
		home:         os.UserHomeDir,
		getenv:       os.Getenv,
		exists: func(p string) bool {
			_, err := os.Stat(p)
			return err == nil
		},
		readFile: os.ReadFile,
		run: func(name string, args ...string) (string, error) {
			ctx, cancel := context.WithTimeout(context.Background(), commandDeadline)
			defer cancel()
			out, err := exec.CommandContext(ctx, name, args...).Output()
			return string(out), err
		},
	}
}

// Detect reports this installation. autoUpdateEnabled is the configuration's
// auto_update.enabled.
func Detect(autoUpdateEnabled bool) Platform {
	return detect(realEnv(), autoUpdateEnabled)
}

func detect(e env, autoUpdateEnabled bool) Platform {
	p := Platform{OS: e.goos, Arch: e.goarch, OSName: osName(e), InstallMethod: installMethod(e)}
	p.AutoUpdate = autoUpdateEnabled && p.InstallMethod == MethodTarball
	return p
}

func installMethod(e env) string {
	exe, err := e.executable()
	if err != nil {
		return MethodUnknown
	}
	if resolved, rerr := e.evalSymlinks(exe); rerr == nil {
		exe = resolved
	}

	if e.goos == "windows" {
		for _, v := range []string{"ProgramFiles", "ProgramW6432", "ProgramFiles(x86)"} {
			root := e.getenv(v)
			if root == "" {
				continue
			}
			// Compared as text, case-insensitively, as Windows paths are.
			dir := strings.ToLower(strings.TrimSuffix(root, `\`) + `\` + windowsInstall + `\`)
			if rest, ok := strings.CutPrefix(strings.ToLower(exe), dir); ok && !strings.Contains(rest, `\`) {
				return MethodMSI
			}
		}
		return MethodUnknown
	}

	if e.exists(selfupdate.PackageManagedMarker) {
		if e.exists(dpkgRecord) {
			return MethodDeb
		}
		return MethodRPM
	}
	if e.goos == "darwin" && e.exists(macReceipt) {
		return MethodPkg
	}
	if home, herr := e.home(); herr == nil && home != "" && underDir(exe, home) {
		return MethodUser
	}
	if underDir(exe, systemVersions) {
		return MethodTarball
	}
	// The CLI path itself is the binary: installed before the versioned
	// layout existed.
	if exe == systemCLI {
		return MethodTarballLegacy
	}
	return MethodUnknown
}

func underDir(path, dir string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != "." && !strings.HasPrefix(rel, "..")
}

func osName(e env) string {
	switch e.goos {
	case "linux":
		data, err := e.readFile(osRelease)
		if err != nil {
			return ""
		}
		return prettyName(data)
	case "darwin":
		out, err := e.run("sw_vers", "-productVersion")
		if err != nil || strings.TrimSpace(out) == "" {
			return "macOS"
		}
		return "macOS " + strings.TrimSpace(out)
	case "windows":
		// "Microsoft Windows [Version 10.0.20348.2700]"
		out, err := e.run("cmd", "/c", "ver")
		if err != nil {
			return "Windows"
		}
		if s := strings.TrimSpace(out); s != "" {
			return s
		}
		return "Windows"
	}
	return ""
}

// prettyName reads PRETTY_NAME (or NAME and VERSION) from os-release.
func prettyName(data []byte) string {
	vals := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		k, v, ok := strings.Cut(line, "=")
		if !ok || strings.HasPrefix(line, "#") {
			continue
		}
		vals[k] = strings.Trim(v, `"'`)
	}
	if v := vals["PRETTY_NAME"]; v != "" {
		return v
	}
	return strings.TrimSpace(vals["NAME"] + " " + vals["VERSION"])
}
