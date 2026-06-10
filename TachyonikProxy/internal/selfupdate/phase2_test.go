// TachyonikProxy
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

package selfupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// ── State ──────────────────────────────────────────────────────────────

func TestState_LoadMissing(t *testing.T) {
	dir := t.TempDir()
	st, err := LoadState(filepath.Join(dir, "no-such-file.json"))
	if err != nil {
		t.Fatal(err)
	}
	if st.Phase != PhaseIdle {
		t.Errorf("missing state file should default to Idle, got %s", st.Phase)
	}
	if len(st.RolledBack) != 0 {
		t.Error("missing state must have empty RolledBack")
	}
}

func TestState_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "update-state.json")

	original := &State{
		Active:                 "1.4.0",
		Previous:               "1.3.0",
		LastAppliedPublishedAt: time.Date(2026, 5, 4, 10, 0, 0, 0, time.UTC),
		LastCheck:              time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC),
		Phase:                  PhaseIdle,
		RolledBack:             []string{"1.4.1"},
	}
	if err := SaveState(path, original); err != nil {
		t.Fatal(err)
	}

	loaded, err := LoadState(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Active != original.Active || loaded.Previous != original.Previous {
		t.Errorf("active/previous mismatch: got %+v", loaded)
	}
	if !loaded.LastAppliedPublishedAt.Equal(original.LastAppliedPublishedAt) {
		t.Errorf("publishedAt mismatch")
	}
	if !loaded.IsRolledBack("1.4.1") {
		t.Error("rolled-back list lost on round-trip")
	}
}

func TestState_AddRolledBackIdempotent(t *testing.T) {
	st := &State{}
	st.AddRolledBack("1.0.0")
	st.AddRolledBack("1.0.0")
	st.AddRolledBack("")
	if len(st.RolledBack) != 1 {
		t.Errorf("expected single entry, got %v", st.RolledBack)
	}
}

func TestState_CorruptFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "update-state.json")
	if err := os.WriteFile(path, []byte("{not json"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadState(path); err == nil {
		t.Fatal("corrupt state file must error rather than silently default")
	}
}

// ── InstallPaths ────────────────────────────────────────────────────────

func TestInstallPaths_VersionDirAndBinary(t *testing.T) {
	p := &InstallPaths{VersionRoot: "/opt/tachyonik/proxy"}
	if got := p.VersionDir("1.4.0"); got != "/opt/tachyonik/proxy/1.4.0" {
		t.Errorf("VersionDir: %s", got)
	}
	if got := p.VersionBinary("1.4.0"); got != "/opt/tachyonik/proxy/1.4.0/tachyonikproxy" {
		t.Errorf("VersionBinary: %s", got)
	}
}

func TestInstallPaths_CurrentVersionFromSymlink(t *testing.T) {
	root := t.TempDir()
	verDir := filepath.Join(root, "1.5.0")
	if err := os.MkdirAll(verDir, 0755); err != nil {
		t.Fatal(err)
	}
	cur := filepath.Join(root, "current")
	if err := os.Symlink(verDir, cur); err != nil {
		t.Fatal(err)
	}
	p := &InstallPaths{VersionRoot: root, CurrentSymlink: cur}
	v, err := p.CurrentVersionFromSymlink()
	if err != nil {
		t.Fatal(err)
	}
	if v != "1.5.0" {
		t.Errorf("got version %q", v)
	}
}

func TestInstallPaths_RejectsSymlinkOutsideRoot(t *testing.T) {
	root := t.TempDir()
	other := t.TempDir()
	cur := filepath.Join(root, "current")
	if err := os.Symlink(other, cur); err != nil {
		t.Fatal(err)
	}
	p := &InstallPaths{VersionRoot: root, CurrentSymlink: cur}
	if _, err := p.CurrentVersionFromSymlink(); err == nil {
		t.Fatal("symlink pointing outside version root must be rejected")
	}
}

func TestInstallPaths_LayoutBootstrapped(t *testing.T) {
	root := t.TempDir()
	p := &InstallPaths{VersionRoot: root, CurrentSymlink: filepath.Join(root, "current")}
	if p.LayoutBootstrapped() {
		t.Fatal("empty dir should not be considered bootstrapped")
	}
	verDir := filepath.Join(root, "1.5.0")
	if err := os.MkdirAll(verDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(verDir, "tachyonikproxy"), []byte("binary"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(verDir, filepath.Join(root, "current")); err != nil {
		t.Fatal(err)
	}
	if !p.LayoutBootstrapped() {
		t.Fatal("expected bootstrapped layout to be detected")
	}
}

// ── Download + Extract ──────────────────────────────────────────────────

func makeTarGz(t *testing.T, contents map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range contents {
		hdr := &tar.Header{
			Name: name,
			Mode: 0755,
			Size: int64(len(body)),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func sha256hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func TestDownloadAndVerify_ChecksHash(t *testing.T) {
	body := []byte("hello world")
	hash := sha256hex(body)

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(body)
	}))
	defer srv.Close()

	dir := t.TempDir()
	path, err := DownloadAndVerify(context.Background(), srv.Client(), srv.URL+"/artifact.tar.gz", dir, hash)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, body) {
		t.Error("downloaded file content mismatch")
	}

	// Mismatch hash → error and file removed.
	_, err = DownloadAndVerify(context.Background(), srv.Client(), srv.URL+"/artifact.tar.gz", dir, strings.Repeat("0", 64))
	if err == nil {
		t.Fatal("hash mismatch must error")
	}
}

func TestDownloadAndVerify_RejectsHTTP(t *testing.T) {
	if _, err := DownloadAndVerify(context.Background(), nil, "http://example.com/artifact", t.TempDir(), strings.Repeat("0", 64)); err == nil {
		t.Fatal("http:// must be rejected")
	}
}

func TestExtractTarGz_FindsBinary(t *testing.T) {
	dir := t.TempDir()
	body := []byte("FAKE-BINARY")
	archive := makeTarGz(t, map[string][]byte{
		"tachyonikproxy-1.4.0/tachyonikproxy": body,
		"tachyonikproxy-1.4.0/config.yaml":    []byte("server:\n  port: 9100\n"),
	})
	archivePath := filepath.Join(dir, "a.tar.gz")
	if err := os.WriteFile(archivePath, archive, 0644); err != nil {
		t.Fatal(err)
	}

	out := filepath.Join(dir, "out")
	binary, err := ExtractTarGz(archivePath, out)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, body) {
		t.Error("extracted binary content mismatch")
	}
}

func TestExtractTarGz_RejectsTraversal(t *testing.T) {
	dir := t.TempDir()
	archive := makeTarGz(t, map[string][]byte{
		"../etc/passwd": []byte("root:x:0:0::/root:/bin/bash\n"),
	})
	archivePath := filepath.Join(dir, "a.tar.gz")
	if err := os.WriteFile(archivePath, archive, 0644); err != nil {
		t.Fatal(err)
	}
	_, err := ExtractTarGz(archivePath, filepath.Join(dir, "out"))
	if err == nil {
		t.Fatal("path traversal must be rejected")
	}
}

func TestExtractTarGz_NoBinary(t *testing.T) {
	dir := t.TempDir()
	archive := makeTarGz(t, map[string][]byte{
		"tachyonikproxy-1.4.0/config.yaml": []byte("hello"),
	})
	archivePath := filepath.Join(dir, "a.tar.gz")
	if err := os.WriteFile(archivePath, archive, 0644); err != nil {
		t.Fatal(err)
	}
	_, err := ExtractTarGz(archivePath, filepath.Join(dir, "out"))
	if err == nil {
		t.Fatal("archive without the binary must error")
	}
}

// ── Bootstrap ──────────────────────────────────────────────────────────

func TestBootstrap_Idempotent(t *testing.T) {
	root := t.TempDir()
	verDir := filepath.Join(root, "1.5.0")
	if err := os.MkdirAll(verDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(verDir, "tachyonikproxy"), []byte("binary"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(verDir, filepath.Join(root, "current")); err != nil {
		t.Fatal(err)
	}
	cliRoot := t.TempDir()
	p := &InstallPaths{
		Mode:           ModeUser,
		VersionRoot:    root,
		CurrentSymlink: filepath.Join(root, "current"),
		CLISymlink:     filepath.Join(cliRoot, "tachyonikproxy"),
		StateFile:      filepath.Join(root, "update-state.json"),
		StagingRoot:    filepath.Join(root, ".staging"),
	}
	res, err := Bootstrap(p, "1.5.0")
	if err != nil {
		t.Fatal(err)
	}
	if !res.AlreadyBootstrapped {
		t.Error("re-running on a bootstrapped layout should be a no-op")
	}
}

func TestBootstrap_MigratesExistingBinary(t *testing.T) {
	root := t.TempDir()
	cliRoot := t.TempDir()
	cli := filepath.Join(cliRoot, "tachyonikproxy")
	binBytes := []byte("ORIG-BINARY")
	if err := os.WriteFile(cli, binBytes, 0755); err != nil {
		t.Fatal(err)
	}

	p := &InstallPaths{
		Mode:           ModeUser,
		VersionRoot:    root,
		CurrentSymlink: filepath.Join(root, "current"),
		CLISymlink:     cli,
		StateFile:      filepath.Join(root, "update-state.json"),
		StagingRoot:    filepath.Join(root, ".staging"),
	}

	res, err := Bootstrap(p, "1.3.0")
	if err != nil {
		t.Fatal(err)
	}
	if res.AlreadyBootstrapped {
		t.Fatal("first bootstrap must not report AlreadyBootstrapped")
	}

	// Version dir contains the binary.
	got, err := os.ReadFile(p.VersionBinary("1.3.0"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, binBytes) {
		t.Error("migrated binary content mismatch")
	}

	// CLI symlink points through current → version dir → binary.
	target, err := os.Readlink(cli)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(target, "current/tachyonikproxy") {
		t.Errorf("CLI symlink target unexpected: %s", target)
	}

	// State file initialized.
	st, err := LoadState(p.StateFile)
	if err != nil {
		t.Fatal(err)
	}
	if st.Active != "1.3.0" {
		t.Errorf("state.Active = %q, want 1.3.0", st.Active)
	}

	// Idempotent re-run.
	res2, err := Bootstrap(p, "1.3.0")
	if err != nil {
		t.Fatal(err)
	}
	if !res2.AlreadyBootstrapped {
		t.Error("second bootstrap must report AlreadyBootstrapped")
	}
}

// ── Apply (with fakes) ─────────────────────────────────────────────────

type fakeService struct {
	restartCalls int32
	failOnCall   int32
}

func (s *fakeService) Name() string { return "fake" }
func (s *fakeService) Restart(ctx context.Context) error {
	n := atomic.AddInt32(&s.restartCalls, 1)
	if s.failOnCall != 0 && n == s.failOnCall {
		return errors.New("simulated restart failure")
	}
	return nil
}

// applyFixture builds a complete environment for an Apply test: server
// hosting the artifact, signed manifest+sig, paths with the current symlink
// already pointing at a "previous" version, and a fake service controller.
type applyFixture struct {
	root         string
	paths        *InstallPaths
	manifestSrv  *httptest.Server
	artifactSHA  string
	priv         ed25519.PrivateKey
	pub          ed25519.PublicKey
	manifestBody []byte
	manifestSig  []byte
	current      string
	target       string
	manifest     Manifest
	httpClient   *http.Client
}

func newApplyFixture(t *testing.T, healthSucceeds bool) *applyFixture {
	t.Helper()
	root := t.TempDir()
	paths := &InstallPaths{
		Mode:           ModeUser,
		VersionRoot:    root,
		CurrentSymlink: filepath.Join(root, "current"),
		CLISymlink:     filepath.Join(t.TempDir(), "tachyonikproxy"), // unused
		StateFile:      filepath.Join(root, "update-state.json"),
		StagingRoot:    filepath.Join(root, ".staging"),
	}

	// Pre-existing version 1.3.0 with a current symlink — simulates a
	// freshly bootstrapped install.
	prevDir := paths.VersionDir("1.3.0")
	if err := os.MkdirAll(prevDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(prevDir, "tachyonikproxy"), []byte("PREV"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(prevDir, paths.CurrentSymlink); err != nil {
		t.Fatal(err)
	}

	// Build artifact tar.gz.
	binary := []byte("NEW-BINARY")
	archiveBytes := makeTarGz(t, map[string][]byte{
		"tachyonikproxy-1.4.0/tachyonikproxy": binary,
	})
	artifactSHA := sha256hex(archiveBytes)

	// Build + sign manifest. We start a TLS server first so we know its URL,
	// then we write the manifest with the artifact URL pointing back at it.
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/artifact.tar.gz", func(w http.ResponseWriter, r *http.Request) {
		w.Write(archiveBytes)
	})
	// Manifest body is filled in after we know the server URL.
	var manifestBody, manifestSig []byte
	mux.HandleFunc("/manifest.json", func(w http.ResponseWriter, r *http.Request) {
		w.Write(manifestBody)
	})
	mux.HandleFunc("/manifest.json.sig", func(w http.ResponseWriter, r *http.Request) {
		w.Write(manifestSig)
	})
	srv := httptest.NewTLSServer(mux)
	t.Cleanup(srv.Close)

	manifest := Manifest{
		SchemaVersion: ManifestSchemaVersion,
		Channel:       "stable",
		LatestVersion: "1.4.0",
		PublishedAt:   time.Date(2026, 5, 4, 10, 0, 0, 0, time.UTC),
		Artifacts: map[string]Artifact{
			"linux/amd64":  {URL: srv.URL + "/artifact.tar.gz", SHA256: artifactSHA},
			"linux/arm64":  {URL: srv.URL + "/artifact.tar.gz", SHA256: artifactSHA},
			"darwin/amd64": {URL: srv.URL + "/artifact.tar.gz", SHA256: artifactSHA},
			"darwin/arm64": {URL: srv.URL + "/artifact.tar.gz", SHA256: artifactSHA},
		},
	}
	body, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	manifestBody = body
	manifestSig = ed25519.Sign(priv, body)

	return &applyFixture{
		root:         root,
		paths:        paths,
		manifestSrv:  srv,
		artifactSHA:  artifactSHA,
		priv:         priv,
		pub:          pub,
		manifestBody: manifestBody,
		manifestSig:  manifestSig,
		manifest:     manifest,
		current:      "1.3.0",
		target:       "1.4.0",
		httpClient:   srv.Client(),
	}
}

func (f *applyFixture) decision() *Decision {
	a := f.manifest.Artifacts["linux/amd64"]
	return &Decision{
		UpdateAvailable: true,
		Manifest:        &f.manifest,
		Artifact:        &a,
		Reason:          "test",
	}
}

// healthFakeServer launches a TLS server we point the health probe at, so
// the apply path's success / failure depends on whether the test starts
// the server.
func healthListenerAddr(t *testing.T, succeed bool) (host, port string) {
	t.Helper()
	if !succeed {
		// Returning a port nothing is listening on — guarantees probe
		// failure within the window.
		return "127.0.0.1", "1" // privileged port, almost certainly not in use
	}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	t.Cleanup(srv.Close)
	// Parse host:port out of the server URL.
	u := srv.URL[len("https://"):]
	colon := strings.LastIndex(u, ":")
	return u[:colon], u[colon+1:]
}

func TestApply_HappyPath(t *testing.T) {
	fx := newApplyFixture(t, true)
	host, port := healthListenerAddr(t, true)

	st := &State{Phase: PhaseIdle, Active: "1.3.0"}
	res, err := Apply(context.Background(), st, ApplyOptions{
		Decision: fx.decision(),
		Paths:    fx.paths,
		Service:  &fakeService{},
		Health: HealthOptions{
			Mode:   HealthModeOutbound,
			Host:   host,
			Port:   port,
			Window: 5 * time.Second,
		},
		HTTPClient: fx.httpClient,
	})
	if err != nil {
		t.Fatalf("Apply errored: %v", err)
	}
	if res.RolledBack {
		t.Error("happy path must not roll back")
	}
	if res.NewVersion != "1.4.0" || res.PreviousVersion != "1.3.0" {
		t.Errorf("unexpected versions: %+v", res)
	}
	v, err := fx.paths.CurrentVersionFromSymlink()
	if err != nil {
		t.Fatal(err)
	}
	if v != "1.4.0" {
		t.Errorf("current symlink not flipped: %s", v)
	}
	stOut, err := LoadState(fx.paths.StateFile)
	if err != nil {
		t.Fatal(err)
	}
	if stOut.Active != "1.4.0" || stOut.Previous != "1.3.0" {
		t.Errorf("state not updated: %+v", stOut)
	}
}

func TestApply_HealthFails_RollsBack(t *testing.T) {
	fx := newApplyFixture(t, false)
	host, port := healthListenerAddr(t, false)

	st := &State{Phase: PhaseIdle, Active: "1.3.0"}
	res, err := Apply(context.Background(), st, ApplyOptions{
		Decision: fx.decision(),
		Paths:    fx.paths,
		Service:  &fakeService{},
		Health: HealthOptions{
			Mode:         HealthModeOutbound,
			Host:         host,
			Port:         port,
			Window:       2 * time.Second,
			PollInterval: 250 * time.Millisecond,
		},
		HTTPClient: fx.httpClient,
	})
	// Apply returns nil error on a clean rollback (the rollback succeeded
	// in restoring the previous version) — the caller distinguishes via
	// res.RolledBack. The health-failure-AND-rollback-failure path is a
	// non-nil error.
	if res == nil {
		t.Fatalf("expected non-nil result; err=%v", err)
	}
	if !res.RolledBack {
		t.Error("expected RolledBack=true")
	}
	v, err := fx.paths.CurrentVersionFromSymlink()
	if err != nil {
		t.Fatal(err)
	}
	if v != "1.3.0" {
		t.Errorf("current should have been rolled back; got %s", v)
	}
	stOut, _ := LoadState(fx.paths.StateFile)
	if !stOut.IsRolledBack("1.4.0") {
		t.Error("rolled-back version not recorded as sticky")
	}
}

func TestApply_StickyRollback_RefusesReapply(t *testing.T) {
	fx := newApplyFixture(t, true)
	host, port := healthListenerAddr(t, true)

	st := &State{Phase: PhaseIdle, Active: "1.3.0"}
	st.AddRolledBack("1.4.0")
	_, err := Apply(context.Background(), st, ApplyOptions{
		Decision: fx.decision(),
		Paths:    fx.paths,
		Service:  &fakeService{},
		Health: HealthOptions{
			Mode:   HealthModeOutbound,
			Host:   host,
			Port:   port,
			Window: 1 * time.Second,
		},
		HTTPClient: fx.httpClient,
	})
	if err == nil {
		t.Fatal("expected sticky rollback to refuse re-apply")
	}
	if !strings.Contains(err.Error(), "rolled back previously") {
		t.Errorf("error message not informative: %v", err)
	}
}
