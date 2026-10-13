// TachyonikProxy
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// The InsightVM built-in: a Rapid7 InsightVM Security Console's host inventory,
// and the test of the credential that reads it.
//
// It follows the GMP built-in in everything the platform sees — the console's
// address comes from the tool installation, the account from the credential
// linked to it, and the inventory leaves as an output file that analysis and
// import rules turn into assets. What differs is the protocol (HTTPS with HTTP
// Basic, package insightvm) and what is pinned: the console's TLS certificate,
// in the same place an appliance's SSH host key is kept.

package tools

import (
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"tachyonik/lib/logger"
	"tachyonik/tachyonikproxy/internal/config"
	"tachyonik/tachyonikproxy/internal/insightvm"
)

// builtinInsightVMGetAssets is the command naming the InsightVM host inventory.
const builtinInsightVMGetAssets = BuiltinPrefix + "insightvm-get-assets"

// InsightVMTarget is how to reach a console, as the call's arguments describe
// it.
type InsightVMTarget struct {
	Host string
	Port int
}

// insightvmTargetFromArgs reads the console's address from the arguments. Only
// the host is required; the port defaults to the console's own.
func insightvmTargetFromArgs(args map[string]interface{}) (InsightVMTarget, error) {
	var t InsightVMTarget
	var err error
	if t.Host, err = hostArg(args); err != nil {
		return t, err
	}
	if t.Port, err = portArg(args, insightvm.DefaultPort); err != nil {
		return t, err
	}
	return t, nil
}

// newInsightVMClient prepares a client with the linked credential and the
// confirmed certificate.
func newInsightVMClient(target InsightVMTarget, secrets Secrets, timeout time.Duration) (*insightvm.Client, error) {
	if secrets.Login == "" {
		return nil, errors.New("no credential is linked to this tool: link one in the tool's settings")
	}
	return insightvm.New(insightvm.Config{
		Host:            target.Host,
		Port:            target.Port,
		Username:        secrets.Login,
		Password:        secrets.Password,
		CertFingerprint: secrets.HostKey,
		Timeout:         timeout,
	}), nil
}

// insightvmGetAssets fetches every asset the console's account may see and
// returns them as an output file.
func insightvmGetAssets(tool *config.ToolConfig, args map[string]interface{}, secrets Secrets) (*ToolResult, error) {
	target, err := insightvmTargetFromArgs(args)
	if err != nil {
		return &ToolResult{Content: err.Error(), IsError: true}, nil
	}

	timeout := time.Duration(tool.Timeout) * time.Second
	if timeout <= 0 {
		timeout = 300 * time.Second
	}
	client, err := newInsightVMClient(target, secrets, timeout)
	if err != nil {
		return &ToolResult{Content: err.Error(), IsError: true}, nil
	}

	// The same cap a binary tool's output has, applied while the document is
	// built.
	limit := tool.MaxOutputBytes
	if limit <= 0 {
		limit = defaultMaxOutputBytes
	}
	client.MaxDocumentBytes = limit

	assets, err := client.HostAssets()
	if err != nil {
		var tooLarge *insightvm.DocumentTooLargeError
		if errors.As(err, &tooLarge) {
			return &ToolResult{
				Content: fmt.Sprintf("the host inventory of %s is larger than this tool's max_output_bytes of %d — raise it in the tool rule",
					target.Host, limit),
				IsError: true,
			}, nil
		}
		return &ToolResult{Content: describeInsightVMError(target, err), IsError: true}, nil
	}

	filename := insightvmAssetsFilename(target.Host, time.Now())
	logger.Infof("insightvm-get-assets: %d asset(s) from %s, %d bytes, as %s", assets.Count, target.Host, len(assets.Document), filename)
	return &ToolResult{
		Content: fmt.Sprintf("Retrieved %d host assets from %s (%d bytes) as %s.",
			assets.Count, target.Host, len(assets.Document), filename),
		OutputFiles: []OutputFile{{
			Filename: filename,
			MimeType: "application/json",
			Data:     base64.StdEncoding.EncodeToString(assets.Document),
		}},
	}, nil
}

// insightvmAssetsFilename names the document after the console and the time it
// was fetched, like gmpAssetsFilename.
func insightvmAssetsFilename(host string, at time.Time) string {
	return fmt.Sprintf("insightvm-host-assets-%s-%s.json", filenameSafe(host), at.UTC().Format("20060102T150405Z"))
}

// describeInsightVMError turns a failure into something an operator can act
// on, saying whose problem it is: a certificate or credential is fixed in
// Tachyonik, an account's role on the console, an unreachable console on the
// network. Never includes the credential.
func describeInsightVMError(target InsightVMTarget, err error) string {
	var uc *insightvm.UnknownCertificateError
	if errors.As(err, &uc) {
		if uc.Expected == "" {
			return fmt.Sprintf("the TLS certificate of %s has not been confirmed yet (%s). Test the credential in the tool's settings and confirm the certificate.", target.Host, uc.Got)
		}
		return fmt.Sprintf("the TLS certificate of %s changed: it is %s, but %s was confirmed. If the console's certificate was replaced, confirm the new one in the tool's settings; otherwise something is impersonating it.", target.Host, uc.Got, uc.Expected)
	}
	if insightvm.IsAuthFailure(err) {
		return fmt.Sprintf("the console at %s refused the credential's username or password (an account with two-factor authentication cannot be used): %v", target.Host, err)
	}
	if insightvm.IsForbidden(err) {
		return fmt.Sprintf("the console at %s accepted the credential, but the account may not read assets: %v", target.Host, err)
	}
	return fmt.Sprintf("could not get assets from %s: %v", target.Host, err)
}

// TestInsightVMCredential checks that the linked credential may read the
// console's assets, without reading more than one.
//
// With no certificate confirmed it stops at the TLS handshake and reports the
// certificate it was offered: the credential is not sent to whatever answered
// at that address until a user has confirmed it is the console.
func TestInsightVMCredential(args map[string]interface{}, secrets Secrets) CredentialTestResult {
	r := testInsightVMCredential(args, secrets)
	r.KeyKind = "tls"
	return r
}

func testInsightVMCredential(args map[string]interface{}, secrets Secrets) CredentialTestResult {
	target, err := insightvmTargetFromArgs(args)
	if err != nil {
		return CredentialTestResult{Message: err.Error()}
	}
	client, err := newInsightVMClient(target, secrets, 20*time.Second)
	if err != nil {
		return CredentialTestResult{Message: err.Error()}
	}

	total, err := client.Probe()
	if err != nil {
		var uc *insightvm.UnknownCertificateError
		if errors.As(err, &uc) {
			return CredentialTestResult{
				HostKeyFingerprint: uc.Got,
				HostKeyUnconfirmed: uc.Expected == "",
				HostKeyMismatch:    uc.Expected != "",
				Message:            describeInsightVMError(target, err),
			}
		}
		return CredentialTestResult{
			AuthFailed:         insightvm.IsAuthFailure(err),
			HostKeyFingerprint: secrets.HostKey,
			Message:            describeInsightVMError(target, err),
		}
	}
	return CredentialTestResult{
		OK:                 true,
		HostKeyFingerprint: secrets.HostKey,
		Message:            fmt.Sprintf("Signed in to %s as %s; the account can read %d assets.", target.Host, secrets.Login, total),
	}
}
