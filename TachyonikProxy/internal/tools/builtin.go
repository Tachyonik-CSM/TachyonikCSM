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
// This file holds what every built-in shares: the dispatch, the secrets a call
// carries, the argument helpers and the credential test's verdict. Each
// built-in has a file of its own beside it (builtin_gmp.go,
// builtin_insightvm.go) that adapts a protocol client package (internal/gmp,
// internal/insightvm) to a tool.
//
// Built-ins are where credentials matter. A binary tool receives only its
// rendered arguments; a built-in additionally receives the credential linked
// to the tool installation, which arrives beside the arguments and never inside
// them — see Secrets.

package tools

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"tachyonik/tachyonikproxy/internal/config"
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
	// HostKey is the key the user confirmed for the installation — an SSH
	// host key for GMP, a TLS certificate for InsightVM, both as "SHA256:…" —
	// or empty when none has been confirmed yet.
	HostKey string
}

// String keeps a Secrets value from printing its contents if one ever reaches
// a formatted log line by accident.
func (s Secrets) String() string { return "Secrets{redacted}" }

// IsBuiltin reports whether a configured tool is answered by the proxy itself.
func IsBuiltin(tool *config.ToolConfig) bool {
	return strings.HasPrefix(tool.Command, BuiltinPrefix)
}

// builtinFunc is the implementation of one built-in.
type builtinFunc func(tool *config.ToolConfig, args map[string]interface{}, secrets Secrets) (*ToolResult, error)

// callBuiltin runs a built-in tool.
//
// Its arguments pass the same checks a binary tool's do — the schema's
// required fields and enums, the allowed_chars allowlist and the flag guard —
// before the built-in sees them. A built-in renders no command line, but the
// allowlist is what the rule's author declared every argument must satisfy,
// and a built-in's own parsing is no reason to skip it.
func callBuiltin(tool *config.ToolConfig, args map[string]interface{}, secrets Secrets) (*ToolResult, error) {
	var run builtinFunc
	switch tool.Command {
	case builtinGMPGetHosts:
		run = gmpGetHosts
	case builtinGMPGetResults:
		run = gmpGetResults
	case builtinInsightVMGetAssets:
		run = insightvmGetAssets
	default:
		return nil, fmt.Errorf("unknown built-in %q", strings.TrimPrefix(tool.Command, BuiltinPrefix))
	}

	if err := ValidateSchema(tool, args); err != nil {
		return &ToolResult{Content: err.Error(), IsError: true, ExitCode: -1}, nil
	}
	if err := ValidateArgs(tool, args); err != nil {
		return &ToolResult{Content: err.Error(), IsError: true, ExitCode: -1}, nil
	}
	return run(tool, args, secrets)
}

// hostArg reads the target's address from the arguments. Every built-in that
// talks to an appliance takes it the same way.
func hostArg(args map[string]interface{}) (string, error) {
	host, _ := args["host"].(string)
	host = strings.TrimSpace(host)
	if host == "" {
		return "", errors.New(`argument "host" is required: the address of the appliance`)
	}
	// A host is an address, not a command line, but it is still checked: it
	// goes into a dial string, and a value carrying a port, a path or spaces is
	// a mistake that should be reported here rather than as a dial failure.
	if strings.ContainsAny(host, " /\\@") {
		return "", fmt.Errorf(`argument "host" is not an address: %q`, host)
	}
	return host, nil
}

// portArg reads an optional port, falling back to def.
func portArg(args map[string]interface{}, def int) (int, error) {
	port := def
	switch v := args["port"].(type) {
	case float64:
		port = int(v)
	case string:
		if v != "" {
			p, err := strconv.Atoi(v)
			if err != nil {
				return 0, fmt.Errorf(`argument "port" is not a number: %q`, v)
			}
			port = p
		}
	}
	if port < 1 || port > 65535 {
		return 0, fmt.Errorf(`argument "port" is out of range: %d`, port)
	}
	return port, nil
}

// filenameSafe keeps letters, digits, dots and dashes of a host and turns
// everything else — an IPv6 address's colons — into dashes.
func filenameSafe(host string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-':
			return r
		default:
			return '-'
		}
	}, host)
}

// CredentialTestResult is the verdict of a credential test, shaped for a UI
// that has to say what to do next.
type CredentialTestResult struct {
	OK bool `json:"ok"`
	// Version is the appliance's protocol version, on success, where the
	// protocol reports one (GMP does).
	Version string `json:"version,omitempty"`
	// KeyKind says what HostKeyFingerprint is: "ssh" for an SSH host key,
	// "tls" for a TLS certificate. The UI words its advice by it.
	KeyKind string `json:"keyKind,omitempty"`
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
