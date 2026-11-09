// TachyonikProxy
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests installation-method and OS detection against the traces each
// installer leaves, supplied as a fake file system.

package platforminfo

import (
	"errors"
	"testing"

	"tachyonik/tachyonikproxy/internal/selfupdate"
)

type fakeFS struct {
	goos  string
	exe   string
	home  string
	vars  map[string]string
	files map[string]string
	cmds  map[string]string
}

func (f fakeFS) env() env {
	return env{
		goos:         f.goos,
		goarch:       "arm64",
		executable:   func() (string, error) { return f.exe, nil },
		evalSymlinks: func(p string) (string, error) { return p, nil },
		home:         func() (string, error) { return f.home, nil },
		getenv:       func(k string) string { return f.vars[k] },
		exists:       func(p string) bool { _, ok := f.files[p]; return ok },
		readFile: func(p string) ([]byte, error) {
			if v, ok := f.files[p]; ok {
				return []byte(v), nil
			}
			return nil, errors.New("not found")
		},
		run: func(name string, args ...string) (string, error) {
			if v, ok := f.cmds[name]; ok {
				return v, nil
			}
			return "", errors.New("no such command")
		},
	}
}

const debianRelease = `PRETTY_NAME="Debian GNU/Linux 13 (trixie)"
NAME="Debian GNU/Linux"
VERSION_ID="13"
`

func TestInstallMethods(t *testing.T) {
	cases := []struct {
		name string
		fs   fakeFS
		want string
	}{
		{"deb", fakeFS{goos: "linux", exe: "/usr/bin/tachyonikproxy", home: "/home/u",
			files: map[string]string{selfupdate.PackageManagedMarker: "", dpkgRecord: ""}}, MethodDeb},
		{"rpm", fakeFS{goos: "linux", exe: "/usr/bin/tachyonikproxy", home: "/home/u",
			files: map[string]string{selfupdate.PackageManagedMarker: ""}}, MethodRPM},
		{"tarball", fakeFS{goos: "linux", exe: "/opt/tachyonik/proxy/1.4.0/tachyonikproxy", home: "/home/u"}, MethodTarball},
		{"tarball on macOS", fakeFS{goos: "darwin", exe: "/opt/tachyonik/proxy/1.4.0/tachyonikproxy", home: "/Users/u"}, MethodTarball},
		{"tarball-legacy", fakeFS{goos: "linux", exe: "/usr/local/bin/tachyonikproxy", home: "/home/u"}, MethodTarballLegacy},
		{"user", fakeFS{goos: "linux", exe: "/home/u/.local/share/tachyonik/proxy/1.4.0/tachyonikproxy", home: "/home/u"}, MethodUser},
		{"pkg", fakeFS{goos: "darwin", exe: "/usr/local/bin/tachyonikproxy", home: "/Users/u",
			files: map[string]string{macReceipt: ""}}, MethodPkg},
		{"msi", fakeFS{goos: "windows", exe: `C:\Program Files\Tachyonik\TachyonikProxy\tachyonikproxy.exe`,
			vars: map[string]string{"ProgramFiles": `C:\Program Files`}}, MethodMSI},
		{"windows zip", fakeFS{goos: "windows", exe: `C:\tools\tachyonikproxy.exe`,
			vars: map[string]string{"ProgramFiles": `C:\Program Files`}}, MethodUnknown},
		{"source tree", fakeFS{goos: "linux", exe: "/srv/build/tachyonikproxy", home: "/home/u"}, MethodUnknown},
	}
	for _, c := range cases {
		if got := installMethod(c.fs.env()); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestAutoUpdateOnlyWhereTheInstallUpdatesItself(t *testing.T) {
	tarball := fakeFS{goos: "linux", exe: "/opt/tachyonik/proxy/1.4.0/tachyonikproxy", home: "/home/u"}
	if !detect(tarball.env(), true).AutoUpdate {
		t.Error("a tarball install with auto_update.enabled does not report autoUpdate")
	}
	if detect(tarball.env(), false).AutoUpdate {
		t.Error("autoUpdate reported although auto_update.enabled is false")
	}
	deb := fakeFS{goos: "linux", exe: "/usr/bin/tachyonikproxy", home: "/home/u",
		files: map[string]string{selfupdate.PackageManagedMarker: "", dpkgRecord: ""}}
	if detect(deb.env(), true).AutoUpdate {
		t.Error("a .deb install reports autoUpdate")
	}
}

func TestOSNames(t *testing.T) {
	linux := fakeFS{goos: "linux", exe: "/x", files: map[string]string{osRelease: debianRelease}}
	if got := osName(linux.env()); got != "Debian GNU/Linux 13 (trixie)" {
		t.Errorf("linux: %q", got)
	}
	noPretty := fakeFS{goos: "linux", exe: "/x", files: map[string]string{osRelease: "NAME=Alpine Linux\nVERSION=3.20\n"}}
	if got := osName(noPretty.env()); got != "Alpine Linux 3.20" {
		t.Errorf("linux without PRETTY_NAME: %q", got)
	}
	unreadable := fakeFS{goos: "linux", exe: "/x"}
	if got := osName(unreadable.env()); got != "" {
		t.Errorf("unreadable os-release: %q, want empty", got)
	}
	mac := fakeFS{goos: "darwin", exe: "/x", cmds: map[string]string{"sw_vers": "15.1\n"}}
	if got := osName(mac.env()); got != "macOS 15.1" {
		t.Errorf("darwin: %q", got)
	}
	win := fakeFS{goos: "windows", exe: "/x", cmds: map[string]string{"cmd": "\r\nMicrosoft Windows [Version 10.0.20348.2700]\r\n"}}
	if got := osName(win.env()); got != "Microsoft Windows [Version 10.0.20348.2700]" {
		t.Errorf("windows: %q", got)
	}
}
