// TachyonikProxy
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"io/fs"
	"os"
	"strings"
	"testing"
)

// "reset-enrollment and re-enroll" is right for a missing file and actively
// wrong for a permission error — re-enrolling as root is what created the
// unreadable material in the first place. The two cases must not share a
// message.
func TestTLSFileErrorMessageDistinguishesPermissionFromMissing(t *testing.T) {
	missing := tlsFileErrorMessage("ca_cert", "/etc/tachyonik/tachyonikproxy/certs/ca.crt", fs.ErrNotExist)
	if !strings.Contains(missing, "reset-enrollment") {
		t.Errorf("a missing file should point at reset-enrollment, got: %s", missing)
	}

	denied := tlsFileErrorMessage("reverse_connect.client_cert",
		"/etc/tachyonik/tachyonikproxy/certs/client.crt",
		&os.PathError{Op: "open", Path: "/etc/tachyonik/tachyonikproxy/certs/client.crt", Err: os.ErrPermission})

	if strings.Contains(denied, "reset-enrollment") {
		t.Errorf("a permission error must not advise re-enrolling — that is how the "+
			"material became unreadable. Got: %s", denied)
	}
	// The message has to carry the actual repair, because the symptom (EACCES on
	// a world-readable file) points nowhere useful on its own.
	if !strings.Contains(denied, "chown -R") {
		t.Errorf("permission message lacks the chown repair: %s", denied)
	}
	// …and it must name the config directory, not the certs subdirectory: the
	// wrong ownership covers the whole tree.
	if !strings.Contains(denied, "/etc/tachyonik/tachyonikproxy\n") &&
		!strings.HasSuffix(strings.TrimSpace(denied), "/etc/tachyonik/tachyonikproxy") {
		t.Errorf("permission message should name the config directory to chown: %s", denied)
	}
}
