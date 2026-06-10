// TachyonikProxy
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

package selfupdate

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// DownloadAndVerify fetches the artifact at url to a file under destDir,
// then verifies its SHA-256 against expectedHex. Returns the path to the
// verified file. The caller is responsible for cleanup.
//
// The expected hash is checked AFTER the file is fully written and synced
// to disk. A streaming-as-we-go check is tempting but would make recovery
// harder: a torn read would leave a partial file on disk and the same
// state-check code would also have to handle that.
func DownloadAndVerify(ctx context.Context, client *http.Client, url, destDir, expectedHex string) (string, error) {
	if !strings.HasPrefix(url, "https://") {
		return "", fmt.Errorf("artifact URL must be https://, got %q", url)
	}
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Minute}
	}

	if err := os.MkdirAll(destDir, 0755); err != nil {
		return "", fmt.Errorf("create dest dir: %w", err)
	}

	// Filename comes from the URL — accepting whatever the manifest
	// pointed at. The bytes will be SHA-checked, so the filename does
	// not gate trust.
	base := filepath.Base(url)
	if base == "" || base == "/" || base == "." {
		base = "artifact"
	}
	dest := filepath.Join(destDir, base)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch artifact: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("fetch artifact: HTTP %d %s", resp.StatusCode, resp.Status)
	}

	f, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return "", fmt.Errorf("create artifact file: %w", err)
	}
	written, err := io.Copy(f, resp.Body)
	if err != nil {
		f.Close()
		os.Remove(dest)
		return "", fmt.Errorf("write artifact: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(dest)
		return "", fmt.Errorf("sync artifact: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(dest)
		return "", fmt.Errorf("close artifact: %w", err)
	}
	_ = written

	if err := VerifyArtifactFile(dest, expectedHex); err != nil {
		os.Remove(dest)
		return "", err
	}

	return dest, nil
}

// ExtractTarGz extracts the tachyonikproxy binary from a tar.gz artifact
// into outDir/tachyonikproxy. Other entries in the archive are ignored.
//
// We deliberately accept only one filename ("tachyonikproxy") and reject
// any path traversal — a tarball that contained "../etc/passwd" would
// otherwise let a compromised release bypass the signature constraints
// (since the SHA-checked tarball can still contain arbitrary paths).
func ExtractTarGz(archivePath, outDir string) (string, error) {
	if err := os.MkdirAll(outDir, 0755); err != nil {
		return "", fmt.Errorf("create out dir: %w", err)
	}

	f, err := os.Open(archivePath)
	if err != nil {
		return "", fmt.Errorf("open archive: %w", err)
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return "", fmt.Errorf("gzip reader: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	const wantBase = "tachyonikproxy"
	dest := filepath.Join(outDir, wantBase)

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", fmt.Errorf("read tar entry: %w", err)
		}

		// Reject paths with "..", absolute paths, or anything except a
		// regular file whose basename equals the expected binary name.
		// We do not extract anything else — config files, service units,
		// etc. — because the apply path doesn't need them.
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		clean := filepath.Clean(hdr.Name)
		if strings.Contains(clean, "..") || filepath.IsAbs(clean) {
			return "", fmt.Errorf("rejecting suspicious tar entry %q", hdr.Name)
		}
		if filepath.Base(clean) != wantBase {
			continue
		}

		out, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
		if err != nil {
			return "", fmt.Errorf("create extracted binary: %w", err)
		}
		if _, err := io.Copy(out, tr); err != nil {
			out.Close()
			os.Remove(dest)
			return "", fmt.Errorf("write extracted binary: %w", err)
		}
		if err := out.Sync(); err != nil {
			out.Close()
			os.Remove(dest)
			return "", fmt.Errorf("sync extracted binary: %w", err)
		}
		if err := out.Close(); err != nil {
			os.Remove(dest)
			return "", fmt.Errorf("close extracted binary: %w", err)
		}

		// Found and extracted the target file.
		return dest, nil
	}

	return "", fmt.Errorf("archive %s does not contain a regular file named %q", archivePath, wantBase)
}
