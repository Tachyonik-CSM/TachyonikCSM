// TachyonikProxy
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// The GMP built-in: an OPENVAS SCAN appliance's host inventory, and the test of
// the credential that reads it.
//
// It adapts package gmp to a tool: the appliance's address comes from the call's
// arguments, the GMP account from the credential linked to the tool
// installation, and the inventory leaves as an output file that analysis and
// import rules turn into assets. What is pinned is the appliance's SSH host key.

package tools

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"tachyonik/lib/logger"
	"tachyonik/tachyonikproxy/internal/config"
	"tachyonik/tachyonikproxy/internal/gmp"
)

// builtinGMPGetHosts is the command naming the OPENVAS SCAN host inventory.
const builtinGMPGetHosts = BuiltinPrefix + "gmp-get-hosts"

// GMPTarget is how to reach an appliance, as the call's arguments describe it.
type GMPTarget struct {
	Host    string
	Port    int
	SSHUser string
}

// gmpTargetFromArgs reads the appliance's address from the arguments.
//
// Only the host is required; the port and the SSH account have the defaults
// every current appliance uses.
func gmpTargetFromArgs(args map[string]interface{}) (GMPTarget, error) {
	t := GMPTarget{SSHUser: gmp.DefaultSSHUser}

	var err error
	if t.Host, err = hostArg(args); err != nil {
		return t, err
	}
	if t.Port, err = portArg(args, gmp.DefaultSSHPort); err != nil {
		return t, err
	}

	if u, _ := args["sshUser"].(string); strings.TrimSpace(u) != "" {
		t.SSHUser = strings.TrimSpace(u)
	}
	return t, nil
}

// DialGMP opens an authenticated GMP session.
//
// The SSH password is deliberately empty. A Greenbone appliance's GMP account
// does not check it — the channel is a gateway into gvmd, and the only
// authentication that means anything is the GMP <authenticate> that follows,
// with the credential's login and password. Verified against Greenbone OS
// 25.0.7: an empty, a wrong and the right SSH password all open the channel,
// and only a wrong GMP password is refused.
func DialGMP(target GMPTarget, secrets Secrets, timeout time.Duration) (*gmp.Client, error) {
	if secrets.Login == "" {
		return nil, errors.New("no credential is linked to this tool: link one in the tool's settings")
	}
	client, err := gmp.DialSSH(gmp.SSHConfig{
		Host:               target.Host,
		Port:               target.Port,
		User:               target.SSHUser,
		Password:           "",
		HostKeyFingerprint: secrets.HostKey,
		Timeout:            timeout,
	})
	if err != nil {
		return nil, err
	}
	if err := client.Authenticate(secrets.Login, secrets.Password); err != nil {
		_ = client.Close()
		return nil, err
	}
	return client, nil
}

// gmpGetHosts fetches every host asset the appliance holds and returns the
// appliance's document unmodified, as an output file.
//
// The file becomes a source, and the platform's analysis and import rules turn
// it into assets, exactly as they do an Nmap or OpenVAS report file. The text
// result only says what was fetched; nothing here interprets the hosts.
func gmpGetHosts(tool *config.ToolConfig, args map[string]interface{}, secrets Secrets) (*ToolResult, error) {
	target, err := gmpTargetFromArgs(args)
	if err != nil {
		return &ToolResult{Content: err.Error(), IsError: true}, nil
	}

	timeout := time.Duration(tool.Timeout) * time.Second
	if timeout <= 0 {
		timeout = 60 * time.Second
	}

	client, err := DialGMP(target, secrets, timeout)
	if err != nil {
		return &ToolResult{Content: describeGMPError(target, err), IsError: true}, nil
	}
	defer client.Close()

	// The same cap a binary tool's output has, applied while reading.
	limit := tool.MaxOutputBytes
	if limit <= 0 {
		limit = defaultMaxOutputBytes
	}
	client.MaxResponseBytes = limit

	assets, err := client.HostAssets()
	if err != nil {
		var tooLarge *gmp.ResponseTooLargeError
		if errors.As(err, &tooLarge) {
			return &ToolResult{
				Content: fmt.Sprintf("the host inventory of %s is larger than this tool's max_output_bytes of %d — raise it in the tool rule",
					target.Host, limit),
				IsError: true,
			}, nil
		}
		return &ToolResult{Content: describeGMPError(target, err), IsError: true}, nil
	}

	filename := gmpAssetsFilename(target.Host, time.Now())
	logger.Infof("gmp-get-hosts: %d host asset(s) from %s, %d bytes, as %s", assets.Count, target.Host, len(assets.Document), filename)
	return &ToolResult{
		Content: fmt.Sprintf("Retrieved %d host assets from %s (%d bytes) as %s.",
			assets.Count, target.Host, len(assets.Document), filename),
		OutputFiles: []OutputFile{{
			Filename: filename,
			MimeType: "application/xml",
			Data:     base64.StdEncoding.EncodeToString(assets.Document),
		}},
	}, nil
}

// gmpAssetsFilename names the document after the appliance and the time it was
// fetched, so successive inventories of one appliance stay apart. Characters a
// filename should not carry — an IPv6 address's colons — become dashes.
func gmpAssetsFilename(host string, at time.Time) string {
	return fmt.Sprintf("openvas-host-assets-%s-%s.xml", filenameSafe(host), at.UTC().Format("20060102T150405Z"))
}

// describeGMPError turns a failure into something an operator can act on. The
// distinction that matters most is whose problem it is: an unconfirmed host key
// and a refused credential are fixed in Tachyonik, an unreachable appliance on
// the network.
//
// Never includes the credential: nothing in this package formats Secrets, and
// the errors it receives come from code that never saw the password in a
// printable position.
func describeGMPError(target GMPTarget, err error) string {
	var hk *gmp.UnknownHostKeyError
	if errors.As(err, &hk) {
		if hk.Expected == "" {
			return fmt.Sprintf("the SSH host key of %s has not been confirmed yet (%s). Test the credential in the tool's settings and confirm the key.", target.Host, hk.Got)
		}
		return fmt.Sprintf("the SSH host key of %s changed: it is %s, but %s was confirmed. If the appliance was reinstalled, confirm the new key in the tool's settings; otherwise something is impersonating it.", target.Host, hk.Got, hk.Expected)
	}
	if gmp.IsAuthFailure(err) {
		return fmt.Sprintf("the appliance at %s refused the credential's username or password", target.Host)
	}
	return fmt.Sprintf("could not get hosts from %s: %v", target.Host, err)
}

// TestGMPCredential checks that the linked credential opens a GMP session on
// the appliance, without doing any work there.
//
// With no host key confirmed it stops before authenticating and reports the
// key it was offered. Sending the password first would hand it to whatever
// answered at that address, which is exactly what confirming the key prevents.
func TestGMPCredential(args map[string]interface{}, secrets Secrets) CredentialTestResult {
	r := testGMPCredential(args, secrets)
	r.KeyKind = "ssh"
	return r
}

func testGMPCredential(args map[string]interface{}, secrets Secrets) CredentialTestResult {
	target, err := gmpTargetFromArgs(args)
	if err != nil {
		return CredentialTestResult{Message: err.Error()}
	}

	client, err := DialGMP(target, secrets, 20*time.Second)
	if err != nil {
		var hk *gmp.UnknownHostKeyError
		if errors.As(err, &hk) {
			return CredentialTestResult{
				HostKeyFingerprint: hk.Got,
				HostKeyUnconfirmed: hk.Expected == "",
				HostKeyMismatch:    hk.Expected != "",
				Message:            describeGMPError(target, err),
			}
		}
		return CredentialTestResult{
			AuthFailed: gmp.IsAuthFailure(err),
			Message:    describeGMPError(target, err),
		}
	}
	defer client.Close()

	version, err := client.Version()
	if err != nil {
		return CredentialTestResult{Message: describeGMPError(target, err)}
	}
	return CredentialTestResult{
		OK:                 true,
		Version:            version,
		HostKeyFingerprint: secrets.HostKey,
		Message:            fmt.Sprintf("Signed in to %s as %s (GMP %s).", target.Host, secrets.Login, version),
	}
}
