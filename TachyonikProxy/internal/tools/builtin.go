// TachyonikProxy
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Built-in tools: capabilities implemented inside the proxy rather than by a
// binary on its host.
//
// A tool rule normally names a command, and the executor runs it. Some tools
// cannot work that way — talking to an OPENVAS SCAN appliance needs the GMP
// protocol, and requiring gvm-tools and a Python runtime on a customer's host
// is not something the proxy can ask for. A rule whose command is
// "builtin:<name>" is answered by Go code here instead, through the same
// tools/list and tools/call surface, so nothing above the proxy has to know the
// difference.
//
// Built-ins are where credentials matter. A binary tool receives only its
// rendered arguments; a built-in additionally receives the credential linked
// to the tool installation, which arrives beside the arguments and never inside
// them — see Secrets.

package tools

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"tachyonik/lib/logger"
	"tachyonik/tachyonikproxy/internal/config"
	"tachyonik/tachyonikproxy/internal/gmp"
)

// BuiltinPrefix marks a tool rule command answered by the proxy itself.
const BuiltinPrefix = "builtin:"

// Secrets is what a call may carry besides its arguments.
//
// Kept out of the arguments map on purpose. Arguments are validated against a
// per-tool character allowlist, rendered into command lines and echoed in
// errors; a password must go through none of that. This struct is passed by
// value to the built-in that needs it and is never logged or written to disk.
type Secrets struct {
	// Login and Password are the credential linked to the tool installation.
	Login    string
	Password string
	// HostKey is the SSH host key fingerprint the user confirmed for the
	// installation, or empty when none has been confirmed yet.
	HostKey string
}

// String keeps a Secrets value from printing its contents if one ever reaches
// a formatted log line by accident.
func (s Secrets) String() string { return "Secrets{redacted}" }

// IsBuiltin reports whether a configured tool is answered by the proxy itself.
func IsBuiltin(tool *config.ToolConfig) bool {
	return strings.HasPrefix(tool.Command, BuiltinPrefix)
}

// builtinGMPGetHosts is the command naming the OPENVAS SCAN host inventory.
const builtinGMPGetHosts = BuiltinPrefix + "gmp-get-hosts"

// callBuiltin runs a built-in tool.
func callBuiltin(tool *config.ToolConfig, args map[string]interface{}, secrets Secrets) (*ToolResult, error) {
	switch tool.Command {
	case builtinGMPGetHosts:
		return gmpGetHosts(tool, args, secrets)
	default:
		return nil, fmt.Errorf("unknown built-in %q", strings.TrimPrefix(tool.Command, BuiltinPrefix))
	}
}

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
	t := GMPTarget{Port: gmp.DefaultSSHPort, SSHUser: gmp.DefaultSSHUser}

	host, _ := args["host"].(string)
	t.Host = strings.TrimSpace(host)
	if t.Host == "" {
		return t, errors.New(`argument "host" is required: the address of the appliance`)
	}
	// A host is an address, not a command line, but it is still checked: it
	// goes into a dial string, and a value carrying a port, a path or spaces is
	// a mistake that should be reported here rather than as a dial failure.
	if strings.ContainsAny(t.Host, " /\\@") {
		return t, fmt.Errorf(`argument "host" is not an address: %q`, t.Host)
	}

	switch v := args["port"].(type) {
	case float64:
		t.Port = int(v)
	case string:
		if v != "" {
			p, err := strconv.Atoi(v)
			if err != nil {
				return t, fmt.Errorf(`argument "port" is not a number: %q`, v)
			}
			t.Port = p
		}
	}
	if t.Port < 1 || t.Port > 65535 {
		return t, fmt.Errorf(`argument "port" is out of range: %d`, t.Port)
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

// gmpHostRecord is one host as the tool returns it. Field names are the ones a
// routine reading the result will see, so they are spelled for that reader.
type gmpHostRecord struct {
	IP       string `json:"ip"`
	Hostname string `json:"hostname,omitempty"`
	OS       string `json:"os,omitempty"`
	LastSeen string `json:"lastSeen,omitempty"`
}

// gmpGetHosts returns every host asset the appliance holds, as JSON.
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

	hosts, err := client.Hosts()
	if err != nil {
		return &ToolResult{Content: describeGMPError(target, err), IsError: true}, nil
	}

	records := make([]gmpHostRecord, 0, len(hosts))
	for _, h := range hosts {
		r := gmpHostRecord{IP: h.IP, Hostname: h.Hostname, OS: h.OS}
		if !h.LastSeen.IsZero() {
			r.LastSeen = h.LastSeen.UTC().Format(time.RFC3339)
		}
		records = append(records, r)
	}

	body, err := json.Marshal(map[string]interface{}{
		"appliance": target.Host,
		"count":     len(records),
		"hosts":     records,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to encode the host list: %w", err)
	}
	if tool.MaxOutputBytes > 0 && len(body) > tool.MaxOutputBytes {
		return &ToolResult{
			Content: fmt.Sprintf("the appliance returned %d hosts (%d bytes), more than this tool's max_output_bytes of %d — raise it in the tool rule",
				len(records), len(body), tool.MaxOutputBytes),
			IsError: true,
		}, nil
	}

	logger.Infof("gmp-get-hosts: %d host(s) from %s", len(records), target.Host)
	return &ToolResult{Content: string(body)}, nil
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

// CredentialTestResult is the verdict of a credential test, shaped for a UI
// that has to say what to do next.
type CredentialTestResult struct {
	OK bool `json:"ok"`
	// Version is the appliance's GMP version, on success.
	Version string `json:"version,omitempty"`
	// HostKeyFingerprint is the key the appliance offered, whenever it got as
	// far as offering one — so an unconfirmed key can be shown and confirmed.
	HostKeyFingerprint string `json:"hostKeyFingerprint,omitempty"`
	// HostKeyUnconfirmed: no key is pinned yet. Confirm the fingerprint above,
	// then test again — the credential is not sent until then.
	HostKeyUnconfirmed bool `json:"hostKeyUnconfirmed,omitempty"`
	// HostKeyMismatch: a key is pinned and the appliance offered another.
	HostKeyMismatch bool `json:"hostKeyMismatch,omitempty"`
	// AuthFailed: the appliance refused the credential's username or password.
	AuthFailed bool `json:"authFailed,omitempty"`
	// Message says the same in words.
	Message string `json:"message"`
}

// TestGMPCredential checks that the linked credential opens a GMP session on
// the appliance, without doing any work there.
//
// With no host key confirmed it stops before authenticating and reports the
// key it was offered. Sending the password first would hand it to whatever
// answered at that address, which is exactly what confirming the key prevents.
func TestGMPCredential(args map[string]interface{}, secrets Secrets) CredentialTestResult {
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
