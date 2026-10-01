// TachyonikProxy
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Pins the guards around built-in tools that do not need an appliance: how the
// target is read from the arguments, that a credential never reaches a binary
// tool, and that one never prints.

package tools

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"tachyonik/tachyonikproxy/internal/config"
)

func TestGMPTargetDefaultsAndValidation(t *testing.T) {
	got, err := gmpTargetFromArgs(map[string]interface{}{"host": " 192.168.178.162 "})
	if err != nil {
		t.Fatalf("a plain host was refused: %v", err)
	}
	if got.Host != "192.168.178.162" || got.Port != 22 || got.SSHUser != "gmp" {
		t.Errorf("defaults not applied: %+v", got)
	}

	got, err = gmpTargetFromArgs(map[string]interface{}{"host": "scan.local", "port": float64(2222), "sshUser": "gvm"})
	if err != nil || got.Port != 2222 || got.SSHUser != "gvm" {
		t.Errorf("explicit values not taken: %+v, %v", got, err)
	}

	for _, bad := range []map[string]interface{}{
		{},
		{"host": ""},
		{"host": "10.0.0.1 -oProxyCommand=x"},
		{"host": "user@10.0.0.1"},
		{"host": "10.0.0.1", "port": float64(0)},
		{"host": "10.0.0.1", "port": float64(70000)},
		{"host": "10.0.0.1", "port": "ssh"},
	} {
		if _, err := gmpTargetFromArgs(bad); err == nil {
			t.Errorf("accepted %v", bad)
		}
	}
}

// A credential linked to a tool that is a binary must not be handed to it: the
// executor renders arguments into a command line, and that path never sees
// Secrets. Proven by giving a binary tool a credential and checking the
// executor ran without it — /bin/echo prints whatever it is given.
func TestBinaryToolsNeverSeeTheCredential(t *testing.T) {
	reg := NewRegistry([]config.ToolConfig{{
		Name: "echo", Command: "/bin/echo", ArgTemplate: "{{.msg}}", Timeout: 5,
		AllowedChars: "a-z",
	}}, nil)

	res, err := reg.CallTool("echo", map[string]interface{}{"msg": "hello"},
		Secrets{Login: "operator", Password: "s3cret-value"})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if strings.Contains(res.Content, "s3cret-value") || strings.Contains(res.Content, "operator") {
		t.Errorf("the credential reached a binary tool: %q", res.Content)
	}
}

func TestSecretsNeverPrint(t *testing.T) {
	s := Secrets{Login: "operator", Password: "s3cret-value", HostKey: "SHA256:abc"}
	for _, out := range []string{fmt.Sprint(s), fmt.Sprintf("%v", s), fmt.Sprintf("%s", s), fmt.Sprintf("%+v", s)} {
		if strings.Contains(out, "s3cret-value") {
			t.Errorf("a formatted Secrets printed the password: %q", out)
		}
	}
}

func TestGMPWithoutACredentialSaysSo(t *testing.T) {
	res, err := gmpGetHosts(&config.ToolConfig{Command: builtinGMPGetHosts},
		map[string]interface{}{"host": "192.0.2.1"}, Secrets{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.IsError || !strings.Contains(res.Content, "no credential") {
		t.Errorf("missing credential not reported plainly: %+v", res)
	}
}

func TestUnknownBuiltinIsRefused(t *testing.T) {
	reg := NewRegistry([]config.ToolConfig{{Name: "mystery", Command: BuiltinPrefix + "nope"}}, nil)
	if _, err := reg.CallTool("mystery", nil, Secrets{}); err == nil {
		t.Error("an unknown built-in was run")
	}
}

// A built-in's arguments pass the same checks as a binary tool's, before the
// built-in sees them. Each refused case below would otherwise reach
// gmpGetHosts, which — with no credential — answers "no credential"; seeing
// that message instead of the check's is how a skipped check shows.
func TestBuiltinArgumentsAreValidated(t *testing.T) {
	gmpTool := config.ToolConfig{
		Name:         "openvas get host assets",
		Command:      builtinGMPGetHosts,
		AllowedChars: `a-zA-Z0-9.:_\-`,
		ArgsSchema:   config.JSONSchemaString(`{"properties":{"host":{"type":"string"},"port":{"type":"integer"}},"required":["host"]}`),
	}
	reg := NewRegistry([]config.ToolConfig{gmpTool}, nil)

	for _, tc := range []struct {
		name string
		args map[string]interface{}
		want string
	}{
		{"a character outside the allowlist", map[string]interface{}{"host": "192.0.2.1;id"}, "disallowed characters"},
		{"a value that looks like a flag", map[string]interface{}{"host": "-oProxyCommand"}, "command-line flag"},
		{"a missing required argument", map[string]interface{}{"port": float64(22)}, `required argument "host"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res, err := reg.CallTool(gmpTool.Name, tc.args, Secrets{})
			if err != nil {
				t.Fatalf("CallTool: %v", err)
			}
			if !res.IsError || !strings.Contains(res.Content, tc.want) {
				t.Errorf("got %+v, want an error mentioning %q", res, tc.want)
			}
		})
	}

	// Valid arguments get through to the built-in.
	res, err := reg.CallTool(gmpTool.Name, map[string]interface{}{"host": "192.0.2.1", "port": float64(22)}, Secrets{})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !strings.Contains(res.Content, "no credential") {
		t.Errorf("valid arguments did not reach the built-in: %+v", res)
	}
}

func TestGMPAssetsFilename(t *testing.T) {
	at := time.Date(2026, 10, 1, 15, 30, 5, 0, time.FixedZone("CEST", 2*3600))
	if got, want := gmpAssetsFilename("192.168.178.162", at), "openvas-host-assets-192.168.178.162-20261001T133005Z.xml"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if got := gmpAssetsFilename("fe80::1", at); strings.ContainsAny(got, ":/") {
		t.Errorf("an IPv6 address left unsafe characters in %q", got)
	}
}
