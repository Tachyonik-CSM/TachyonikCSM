// TachyonikProxy
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

package tools

import (
	"runtime"
	"strings"
	"testing"

	"tachyonik/tachyonikproxy/internal/config"
)

// TestExecuteTool_NoShellExpansion is the canary for H1. Before the fix,
// configuring rlimits caused the tool to be wrapped in /bin/sh -c, which
// allowed shell metacharacters in arguments to be interpreted by the shell.
// After the fix, the tool is exec'd directly and any metacharacter in an
// argument is passed literally as one argv element.
//
// We exercise this with /bin/echo and an argument containing both a
// semicolon and a `$()` substitution form. If a shell were involved, the
// semicolon would split into two commands and the $() would attempt
// command substitution. With direct exec, /bin/echo prints the argument
// byte-for-byte.
func TestExecuteTool_NoShellExpansion(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test relies on /bin/echo")
	}
	hostile := "; echo PWNED $(uname)"
	tool := &config.ToolConfig{
		Name:        "echo",
		Command:     "/bin/echo",
		ArgTemplate: "{{.payload}}",
		// rlimits set — this is the case where the OLD code wrapped in /bin/sh -c
		MaxCPUSeconds: 10,
		MaxMemoryMB:   64,
		// Deliberately no allowed_chars: the OLD code's check would have
		// permitted the hostile string since the allowlist was empty. The
		// fix decouples safety from the allowlist.
	}
	res, err := ExecuteTool(tool, map[string]interface{}{"payload": hostile})
	if err != nil {
		t.Fatalf("ExecuteTool: %v", err)
	}
	out := strings.TrimSpace(res.Content)

	// The literal string must round-trip. If a shell were in the chain
	// the output would be split or expanded.
	if !strings.Contains(out, hostile) {
		t.Errorf("output does not contain literal payload\n  got:  %q\n  want: contains %q", out, hostile)
	}
	if strings.Contains(out, "PWNED") && !strings.Contains(hostile, "PWNED") {
		// PWNED only appears in the hostile string itself (which echo prints).
		// If a shell ran `echo PWNED`, we'd see it printed on its own line.
		// This branch is here for completeness in case future refactors
		// reintroduce the bug.
		t.Errorf("suspect shell expansion: %q", out)
	}
}

// TestExecuteTool_HandlesSpaceInArgument verifies that splitArgs joins
// quoted args correctly. A literal space inside a quoted argument should
// stay together as one argv element. (Regression risk: any rewrite of
// splitArgs in response to L4 must not break this.)
func TestExecuteTool_HandlesSpaceInArgument(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test relies on /bin/echo")
	}
	tool := &config.ToolConfig{
		Name:        "echo",
		Command:     "/bin/echo",
		ArgTemplate: `"{{.payload}}"`,
	}
	res, err := ExecuteTool(tool, map[string]interface{}{"payload": "hello world"})
	if err != nil {
		t.Fatalf("ExecuteTool: %v", err)
	}
	if got := strings.TrimSpace(res.Content); got != "hello world" {
		t.Errorf("got %q, want %q", got, "hello world")
	}
}

// validateAll mirrors the order ExecuteTool applies its gates: schema
// (required + enum) then the allowed_chars allowlist + flag-injection guard.
func validateAll(tool *config.ToolConfig, args map[string]interface{}) error {
	if err := ValidateSchema(tool, args); err != nil {
		return err
	}
	return ValidateArgs(tool, args)
}

func nmapTool() *config.ToolConfig {
	return &config.ToolConfig{
		Name:         "nmap_scan",
		Command:      "nmap",
		ArgTemplate:  "{{.scan_type}} {{if .ports}}-p {{.ports}}{{end}} {{.target}}",
		AllowedChars: `a-zA-Z0-9.:/\-_, `,
		ArgsSchema:   config.JSONSchemaString(`{"properties":{"target":{"type":"string"},"scan_type":{"enum":["-sS","-sT","-sU","-sV","-A"]},"ports":{"type":"string"}},"required":["target"]}`),
	}
}

func searchsploitTool() *config.ToolConfig {
	return &config.ToolConfig{
		Name:         "searchsploit",
		Command:      "searchsploit",
		ArgTemplate:  "{{if .json_output}}-j{{end}} {{.query}}",
		AllowedChars: `a-zA-Z0-9. -_`,
		ArgsSchema:   config.JSONSchemaString(`{"properties":{"query":{"type":"string"},"json_output":{"type":"boolean"}},"required":["query"]}`),
	}
}

// TestArgValidation_LegitInputsPass guards against the security fix breaking
// the four shipped tools' real usage — including the two awkward cases:
// nmap scan_type is a leading-dash value (allowed via enum), and searchsploit
// query legitimately contains spaces (multiple search terms).
func TestArgValidation_LegitInputsPass(t *testing.T) {
	cases := []struct {
		name string
		tool *config.ToolConfig
		args map[string]interface{}
	}{
		{"nmap full", nmapTool(), map[string]interface{}{"target": "10.0.0.1", "scan_type": "-sV", "ports": "1-1024"}},
		{"nmap cidr", nmapTool(), map[string]interface{}{"target": "192.168.0.0/24"}},
		{"nmap ports list", nmapTool(), map[string]interface{}{"target": "host.local", "ports": "22,80,443"}},
		{"searchsploit multi-term", searchsploitTool(), map[string]interface{}{"query": "apache 2.4"}},
		{"searchsploit json", searchsploitTool(), map[string]interface{}{"query": "linux kernel", "json_output": true}},
	}
	for _, c := range cases {
		if err := validateAll(c.tool, c.args); err != nil {
			t.Errorf("%s: legit args rejected: %v", c.name, err)
		}
	}
}

// TestArgValidation_InjectionBlocked is the core regression guard for the
// argument/flag-injection finding: an arg value must not be able to introduce
// extra command-line flags, whether via an embedded space or a leading dash,
// and an enum field must not accept a value outside its set.
func TestArgValidation_InjectionBlocked(t *testing.T) {
	cases := []struct {
		name string
		tool *config.ToolConfig
		args map[string]interface{}
	}{
		{"nmap target injects --script", nmapTool(), map[string]interface{}{"target": "10.0.0.1 --script=http-evil"}},
		{"nmap ports injects -oN", nmapTool(), map[string]interface{}{"target": "10.0.0.1", "ports": "1-1024 -oN/tmp/x"}},
		{"nmap scan_type out of enum", nmapTool(), map[string]interface{}{"target": "10.0.0.1", "scan_type": "--privileged"}},
		{"searchsploit query injects -o", searchsploitTool(), map[string]interface{}{"query": "apache -o /etc/passwd"}},
		{"bare leading-dash value", nmapTool(), map[string]interface{}{"target": "-oN/tmp/pwn"}},
	}
	for _, c := range cases {
		if err := validateAll(c.tool, c.args); err == nil {
			t.Errorf("%s: expected rejection, got nil", c.name)
		}
	}
}

func TestExecuteTool_FlagInjectionRejected(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test relies on /bin/echo")
	}
	tool := &config.ToolConfig{
		Name:         "echo",
		Command:      "/bin/echo",
		ArgTemplate:  "{{.x}}",
		AllowedChars: `a-zA-Z0-9 -`,
	}
	res, err := ExecuteTool(tool, map[string]interface{}{"x": "--evil"})
	if err != nil {
		t.Fatalf("ExecuteTool: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected IsError for flag-injection arg, got %+v", res)
	}
}

func TestValidExtension(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{".xml", true},
		{".json", true},
		{".tar", true},
		{"", false},
		{"xml", false},          // missing leading dot
		{".", false},            // dot only
		{".xml; rm -rf", false}, // metacharacters
		{".xml ", false},        // trailing space
		{". xml", false},        // embedded space
		{".x/y", false},         // path separator
	}
	for _, c := range cases {
		if got := validExtension(c.in); got != c.want {
			t.Errorf("validExtension(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestValidArgFlag(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"-oX", true},
		{"-h", true},
		{"--output", true},
		{"--output-file", true},
		{"", false},
		{"oX", false},    // missing leading dash
		{"-", false},     // dash only
		{"-oX;", false},  // metacharacter
		{"-o X", false},  // embedded space
		{"-o\nX", false}, // newline
	}
	for _, c := range cases {
		if got := validArgFlag(c.in); got != c.want {
			t.Errorf("validArgFlag(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}
