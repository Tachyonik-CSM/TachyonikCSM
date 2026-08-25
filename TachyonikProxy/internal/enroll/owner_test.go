// TachyonikProxy
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !windows

package enroll

import (
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"tachyonik/tachyonikproxy/internal/config"
)

// ownerOf reads a path's uid/gid for assertions.
func ownerOf(t *testing.T, path string) (int, int) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Skip("no unix stat available")
	}
	return int(st.Uid), int(st.Gid)
}

// The whole point of deriving the owner from the *directory* is that a machine
// already broken by a root enrollment — config.yaml now root-owned — must not
// have that breakage faithfully reproduced by the next enrollment. Assert the
// directory wins over the file.
func TestTargetOwnerPrefersTheDirectoryOverTheConfigFile(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("chowning to another account requires root")
	}
	uid, gid := borrowedAccount(t)

	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("x"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	// The directory belongs to the service account; the file was left root-
	// owned by an earlier broken enrollment.
	if err := os.Chown(dir, uid, gid); err != nil {
		t.Fatalf("chown dir: %v", err)
	}

	owner, ok := targetOwner(dir)
	if !ok {
		t.Fatal("targetOwner reported nothing to do for a non-root-owned config dir")
	}
	if owner.UID != uid || owner.GID != gid {
		t.Errorf("owner = %d:%d, want the directory's %d:%d (not the file's 0:0)",
			owner.UID, owner.GID, uid, gid)
	}
}

// Nothing to do when we are not root: a chown would fail, and an unprivileged
// operator enrolling their own XDG config already owns everything.
func TestTargetOwnerIsANoOpForNonRoot(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("assertion is about the non-root case")
	}
	if _, ok := targetOwner(t.TempDir()); ok {
		t.Error("targetOwner must report nothing to do when not running as root")
	}
}

// Root enrolling a root-owned directory is the macOS LaunchDaemon layout and
// the plain "no service account" case. Chowning root to root is pointless work
// that would still have to be verified afterwards.
func TestTargetOwnerIsANoOpWhenTheDirectoryIsRootOwned(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root to observe a root-owned directory as root")
	}
	if _, ok := targetOwner(t.TempDir()); ok {
		t.Error("root-owned config dir must report nothing to do")
	}
}

// The defect in full: enrollment run under sudo left <config-dir>/certs as
// root:root 0700 while the service runs as its own account, so it could not
// traverse into the directory and every cert inside came back EACCES.
func TestPersistEnrollmentGivesEverythingToTheServiceAccount(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("reproducing the sudo-enrollment layout requires root")
	}
	uid, gid := borrowedAccount(t)

	dir := t.TempDir()
	if err := os.Chown(dir, uid, gid); err != nil {
		t.Fatalf("chown dir: %v", err)
	}
	configPath := filepath.Join(dir, "config.yaml")
	certDir := filepath.Join(dir, "certs")

	cfg := config.LoadFrom(configPath)
	// Inbound: client.crt / client.key are the pair the reverse-connect dialer
	// failed to open in the field report.
	resp := &Response{
		ProxyName:      "p",
		ConnectionMode: "inbound",
		CACert:         "CA",
		ClientCert:     "CRT",
		ClientKey:      "KEY",
	}

	res, err := PersistEnrollment(cfg, resp, certDir, configPath, "csm.example.com")
	if err != nil {
		t.Fatalf("PersistEnrollment: %v", err)
	}
	if res.Owner == "" {
		t.Error("PersistResult.Owner is empty; the enrollment summary would not " +
			"tell the operator who can read the material")
	}

	// The directory matters as much as the files: 0700 owned by the wrong
	// account is what actually blocked access, not the file modes.
	for _, p := range []string{
		certDir,
		configPath,
		filepath.Join(certDir, "ca.crt"),
		filepath.Join(certDir, "client.crt"),
		filepath.Join(certDir, "client.key"),
	} {
		gotUID, gotGID := ownerOf(t, p)
		if gotUID != uid || gotGID != gid {
			t.Errorf("%s owned by %d:%d, want the service account %d:%d", p, gotUID, gotGID, uid, gid)
		}
	}

	// Ownership is the fix, not a relaxation: the directory stays 0700 and the
	// key stays 0600.
	if info, err := os.Stat(certDir); err == nil && info.Mode().Perm() != 0o700 {
		t.Errorf("cert dir mode = %v, want 0700", info.Mode().Perm())
	}
	if info, err := os.Stat(filepath.Join(certDir, "client.key")); err == nil && info.Mode().Perm() != 0o600 {
		t.Errorf("client.key mode = %v, want 0600", info.Mode().Perm())
	}
}

// A root enrollment must also repair config.yaml, which SaveConfig rewrites as
// the calling user. It survives today only because the package happens to ship
// it 0644; SaveConfig's default for a new file is 0640, which would lock the
// service out of its own config.
func TestPersistEnrollmentRepairsARootOwnedConfigFile(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root")
	}
	uid, gid := borrowedAccount(t)

	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("connection_mode: outbound\n"), 0o640); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if err := os.Chown(dir, uid, gid); err != nil {
		t.Fatalf("chown dir: %v", err)
	}
	// config.yaml deliberately left root-owned, as an earlier broken
	// enrollment would have left it.

	cfg := config.LoadFrom(configPath)
	resp := &Response{ProxyName: "p", ConnectionMode: "outbound", CACert: "CA", ServerCert: "CRT", ServerKey: "KEY"}
	if _, err := PersistEnrollment(cfg, resp, filepath.Join(dir, "certs"), configPath, "h"); err != nil {
		t.Fatalf("PersistEnrollment: %v", err)
	}

	gotUID, gotGID := ownerOf(t, configPath)
	if gotUID != uid || gotGID != gid {
		t.Errorf("config.yaml owned by %d:%d, want %d:%d", gotUID, gotGID, uid, gid)
	}
}

// borrowedAccount returns a real non-root uid/gid to stand in for the service
// account. A synthetic number would work for chown but not for the username
// lookup in the operator-facing output.
func borrowedAccount(t *testing.T) (int, int) {
	t.Helper()
	for _, name := range []string{"nobody", "daemon", "bin"} {
		out, err := exec.Command("id", "-u", name).Output()
		if err != nil {
			continue
		}
		uid, err := strconv.Atoi(strings.TrimSpace(string(out)))
		if err != nil || uid == 0 {
			continue
		}
		out, err = exec.Command("id", "-g", name).Output()
		if err != nil {
			continue
		}
		gid, err := strconv.Atoi(strings.TrimSpace(string(out)))
		if err != nil {
			continue
		}
		return uid, gid
	}
	t.Skip("no unprivileged account available to stand in for the service user")
	return 0, 0
}

// The enrollment token is consumed by ResourceManager the moment the request
// arrives (ConsumeEnrollmentToken, at the top of the /api/proxy-enroll
// handler). Failing on the first MkdirAll *after* the POST therefore cost the
// operator a token for nothing and left the platform believing the proxy was
// enrolled. The preflight must run before any request goes out.
func TestRunDoesNotSpendTheTokenWhenItCannotWrite(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory modes")
	}

	var called bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.Write([]byte(`{"proxyName":"p","connectionMode":"outbound","caCert":"CA","serverCert":"C","serverKey":"K"}`))
	}))
	defer srv.Close()

	// A config directory we cannot write, standing in for /etc owned by the
	// service account.
	base := t.TempDir()
	dir := filepath.Join(base, "etc")
	if err := os.Mkdir(dir, 0o500); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o700) })

	err := Run(Options{
		EnrollmentURL: srv.URL + "/?token=abc",
		ConfigPath:    filepath.Join(dir, "config.yaml"),
		Insecure:      true, // srv is plain HTTP
	})
	if err == nil {
		t.Fatal("enrollment into an unwritable directory must fail")
	}
	if called {
		t.Error("the enrollment endpoint was contacted despite the target being unwritable; " +
			"the one-time token would have been spent for nothing")
	}
	if !strings.Contains(err.Error(), "sudo") {
		t.Errorf("error %q does not tell the operator to use sudo", err)
	}
}

// Listen mode reaches PersistEnrollment through the same path, and is the worse
// place to discover the problem: the operator has already run the handshake
// with the platform by the time the write fails. Assert the preflight runs
// before the listener binds, so the failure is immediate.
func TestRunListenRefusesBeforeBindingWhenItCannotWrite(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory modes")
	}

	base := t.TempDir()
	dir := filepath.Join(base, "etc")
	if err := os.Mkdir(dir, 0o500); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o700) })

	// Occupy the port RunListen would bind, so a listener that gets that far
	// fails with a bind error rather than blocking the test for ten minutes.
	ln, err := listenOnFreePort()
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	err = RunListen(ListenOptions{
		ConfigPath: filepath.Join(dir, "config.yaml"),
		Port:       ln.Addr().(*net.TCPAddr).Port,
		Force:      true,
	})
	if err == nil {
		t.Fatal("listen-mode enrollment into an unwritable directory must fail")
	}
	if strings.Contains(err.Error(), "bind") || strings.Contains(err.Error(), "address already in use") {
		t.Errorf("RunListen bound the port before checking it could write: %v", err)
	}
	if !strings.Contains(err.Error(), "sudo") {
		t.Errorf("error %q does not tell the operator to use sudo", err)
	}
}

func listenOnFreePort() (*net.TCPListener, error) {
	addr, err := net.ResolveTCPAddr("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	return net.ListenTCP("tcp", addr)
}

// The preflight must not reject a perfectly writable target — the common case
// is an unprivileged operator enrolling their own config, and a false positive
// there blocks enrollment entirely.
func TestPreflightAcceptsAWritableTarget(t *testing.T) {
	dir := t.TempDir()
	if err := preflightWritable(filepath.Join(dir, "config.yaml"), ""); err != nil {
		t.Errorf("preflight rejected a writable directory: %v", err)
	}
}

// The two ends of this fix live in different files and drift silently: the unit
// declares the service account, the enroller has to hand the material to it.
// Delete the ownership step and nothing fails until a customer's proxy will not
// start — so assert the pairing directly.
func TestServiceAccountAndEnrollerStayInStep(t *testing.T) {
	unit, err := os.ReadFile("../../packaging/systemd/tachyonikproxy.service")
	if err != nil {
		t.Skipf("unit file not readable from here: %v", err)
	}

	var svcUser string
	for _, line := range strings.Split(string(unit), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "User="); ok {
			svcUser = strings.TrimSpace(v)
		}
	}
	if svcUser == "" || svcUser == "root" {
		t.Skip("the packaged unit runs as root; there is no privilege split to bridge")
	}

	// The unit runs as someone other than the enroller, so PersistEnrollment
	// must be doing something about ownership.
	src, err := os.ReadFile("enroll.go")
	if err != nil {
		t.Fatalf("read enroll.go: %v", err)
	}
	if !strings.Contains(string(src), "chownTo(") {
		t.Fatalf("packaging/systemd/tachyonikproxy.service runs as %q, but PersistEnrollment "+
			"no longer chowns anything — enrollment under sudo will leave the cert "+
			"directory unreadable by the service", svcUser)
	}

	// And the postinstall has to create that account, or the chown target does
	// not exist on a fresh install.
	post, err := os.ReadFile("../../packaging/scripts/postinstall.sh")
	if err != nil {
		t.Skipf("postinstall.sh not readable from here: %v", err)
	}
	if !strings.Contains(string(post), svcUser) {
		t.Errorf("postinstall.sh never mentions the service account %q the unit runs as", svcUser)
	}
}

// A first user-space enrollment creates ~/.config/tachyonik/tachyonikproxy on
// the way. Probing a directory that is merely absent reports EACCES the same as
// one that is forbidden, which would have turned every fresh install into a
// bogus "run this with sudo".
func TestPreflightAcceptsADirectoryThatDoesNotExistYet(t *testing.T) {
	deep := filepath.Join(t.TempDir(), "config", "tachyonik", "tachyonikproxy", "config.yaml")
	if err := preflightWritable(deep, ""); err != nil {
		t.Errorf("preflight rejected a not-yet-created config directory: %v", err)
	}
}
