// TachyonikProxy
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

package tools

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"text/template"
	"time"
	"unicode/utf8"

	"tachyonik/tachyonikproxy/internal/config"
	"tachyonik/tachyonikproxy/internal/execenv"
	"tachyonik/tachyonikproxy/internal/logger"
)

const (
	defaultTimeout        = 120
	defaultMaxOutputBytes = 10 * 1024 * 1024 // 10MB
)

type OutputFile struct {
	Filename string `json:"filename"` // e.g. "nmap_scan.xml"
	MimeType string `json:"mimeType"` // e.g. "application/xml"
	Data     string `json:"data"`     // base64-encoded content
}

type ToolResult struct {
	Content     string       `json:"content"`
	IsError     bool         `json:"isError,omitempty"`
	ExitCode    int          `json:"exitCode"`
	OutputFiles []OutputFile `json:"outputFiles,omitempty"`
}

// ValidateArgs validates tool arguments against the allowed_chars allowlist
// and guards against argument (flag) injection.
//
// The allowed_chars allowlist (when set) is necessary but NOT sufficient: the
// shipped configs allow spaces and dashes, and because the rendered template
// is re-tokenised on whitespace (see splitArgs), a value such as
// "10.0.0.1 --script=evil" would otherwise expand into extra argv flags the
// tool author never intended — a real injection even though no shell is
// involved. So, in addition to the character allowlist, every whitespace-
// separated field of a value must not begin with '-'. Values constrained by a
// JSON-schema `enum` are exempt because they are author-approved and may
// legitimately BE flags (e.g. nmap scan_type "-sV"); enum membership itself is
// enforced in ValidateSchema.
func ValidateArgs(tool *config.ToolConfig, args map[string]interface{}) error {
	var pattern *regexp.Regexp
	if tool.AllowedChars != "" {
		p, err := regexp.Compile("^[" + tool.AllowedChars + "]*$")
		if err != nil {
			return fmt.Errorf("invalid allowed_chars pattern: %w", err)
		}
		pattern = p
	}

	enumFields := enumProperties(tool)

	for key, val := range args {
		s := fmt.Sprintf("%v", val)
		if pattern != nil && !pattern.MatchString(s) {
			return fmt.Errorf("argument %q contains disallowed characters", key)
		}
		if enumFields[key] {
			continue
		}
		for _, field := range strings.Fields(s) {
			if strings.HasPrefix(field, "-") {
				return fmt.Errorf("argument %q contains a value that looks like a command-line flag (%q); refusing to prevent flag injection", key, field)
			}
		}
	}
	return nil
}

// enumProperties returns the set of arg names whose JSON-schema property
// declares an `enum`. Such values are validated for membership in
// ValidateSchema and are exempt from the flag-injection guard in ValidateArgs.
func enumProperties(tool *config.ToolConfig) map[string]bool {
	out := map[string]bool{}
	raw := tool.ArgsSchema.RawMessage()
	if len(raw) == 0 {
		return out
	}
	var schema struct {
		Properties map[string]struct {
			Enum []interface{} `json:"enum"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		return out
	}
	for name, prop := range schema.Properties {
		if len(prop.Enum) > 0 {
			out[name] = true
		}
	}
	return out
}

// RenderArgs renders the arg_template with the provided arguments.
func RenderArgs(argTemplate string, args map[string]interface{}) (string, error) {
	tmpl, err := template.New("args").Parse(argTemplate)
	if err != nil {
		return "", fmt.Errorf("failed to parse arg template: %w", err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, args); err != nil {
		return "", fmt.Errorf("failed to execute arg template: %w", err)
	}

	return strings.TrimSpace(buf.String()), nil
}

// ValidateSchema validates args against the tool's JSON schema (basic validation).
func ValidateSchema(tool *config.ToolConfig, args map[string]interface{}) error {
	raw := tool.ArgsSchema.RawMessage()
	if len(raw) == 0 {
		return nil
	}

	var schema struct {
		Properties map[string]struct {
			Enum []interface{} `json:"enum"`
		} `json:"properties"`
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		return nil // skip validation if schema can't be parsed
	}

	for _, req := range schema.Required {
		val, ok := args[req]
		if !ok || val == nil || fmt.Sprintf("%v", val) == "" {
			return fmt.Errorf("required argument %q is missing", req)
		}
	}

	// Enforce enum membership. The schema declares enums (e.g. nmap
	// scan_type) to constrain a value to an author-approved set; without
	// enforcement a caller could pass an arbitrary flag in an "enum" field
	// and bypass the flag-injection guard, which exempts enum fields.
	for name, prop := range schema.Properties {
		if len(prop.Enum) == 0 {
			continue
		}
		val, ok := args[name]
		if !ok {
			continue // absent optional arg; required-check handles missing
		}
		s := fmt.Sprintf("%v", val)
		matched := false
		for _, e := range prop.Enum {
			if fmt.Sprintf("%v", e) == s {
				matched = true
				break
			}
		}
		if !matched {
			return fmt.Errorf("argument %q must be one of the allowed values", name)
		}
	}

	return nil
}

// ExecuteTool runs a configured tool with the given arguments.
func ExecuteTool(tool *config.ToolConfig, args map[string]interface{}) (*ToolResult, error) {
	logger.Infof("Executing tool %q with args: %v", tool.Name, args)

	if err := ValidateSchema(tool, args); err != nil {
		return &ToolResult{Content: err.Error(), IsError: true, ExitCode: -1}, nil
	}

	if err := ValidateArgs(tool, args); err != nil {
		return &ToolResult{Content: err.Error(), IsError: true, ExitCode: -1}, nil
	}

	rendered, err := RenderArgs(tool.ArgTemplate, args)
	if err != nil {
		return &ToolResult{Content: err.Error(), IsError: true, ExitCode: -1}, nil
	}

	// Inject output-file arguments. We use os.CreateTemp (which under the
	// hood uses crypto/rand-seeded entropy and O_EXCL) rather than building
	// a temp path by hand from rand.Read — that avoids both the predictable-
	// path-on-rand-failure footgun and the symlink-race window between
	// path generation and the tool opening the file. The pre-created file
	// is harmless to the tool (it'll truncate-on-open anyway) and we get
	// the unforgeable filename for free.
	//
	// Always clean up via defer — even on error paths or panics — so
	// tools/call timeouts don't leak files into /tmp.
	type pendingFile struct {
		TempPath string
		Config   config.OutputFileConfig
	}
	var pendingFiles []pendingFile
	cleanupTemps := func() {
		for _, pf := range pendingFiles {
			os.Remove(pf.TempPath)
		}
	}
	defer cleanupTemps()
	for _, ofc := range tool.OutputFiles {
		// Validate the configured suffix at use-time as defense in depth
		// against a config push that smuggled shell metacharacters into
		// extension. The pattern matches a leading dot and alphanumerics
		// only.
		if !validExtension(ofc.Extension) {
			return &ToolResult{
				Content:  fmt.Sprintf("invalid output_files.extension %q", ofc.Extension),
				IsError:  true,
				ExitCode: -1,
			}, nil
		}
		if !validArgFlag(ofc.ArgFlag) {
			return &ToolResult{
				Content:  fmt.Sprintf("invalid output_files.arg_flag %q", ofc.ArgFlag),
				IsError:  true,
				ExitCode: -1,
			}, nil
		}
		f, err := os.CreateTemp("", "tachyonikproxy_*"+ofc.Extension)
		if err != nil {
			return &ToolResult{
				Content:  fmt.Sprintf("failed to create temp output file: %v", err),
				IsError:  true,
				ExitCode: -1,
			}, nil
		}
		tmpPath := f.Name()
		f.Close() // tool will reopen and truncate

		rendered += fmt.Sprintf(" %s %s", ofc.ArgFlag, tmpPath)
		pendingFiles = append(pendingFiles, pendingFile{TempPath: tmpPath, Config: ofc})
	}

	timeout := tool.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}

	maxOutput := tool.MaxOutputBytes
	if maxOutput <= 0 {
		maxOutput = defaultMaxOutputBytes
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeout)*time.Second)
	defer cancel()

	// Split rendered args respecting quotes
	cmdArgs := splitArgs(rendered)

	cmd := exec.CommandContext(ctx, tool.Command, cmdArgs...)
	// Run with a minimal, sanitized environment so the proxy's own secrets
	// (enrollment material, tokens) are never inherited by executed tools.
	cmd.Env = execenv.MinimalEnv()
	// Place the tool in its own process group and SIGKILL the whole group on
	// timeout, so a cancelled call cannot leave detached grandchildren running.
	setProcessGroup(cmd)
	cmd.Cancel = func() error { return killProcessGroup(cmd) }
	logger.Infof("Tool %q command line: %s %s", tool.Name, tool.Command, strings.Join(cmdArgs, " "))

	// Bounded buffers so a tool emitting unbounded output is truncated as it
	// streams, rather than buffered whole in proxy memory and truncated only
	// after Wait (which defeats MaxOutputBytes as a memory bound).
	stdout := &boundedBuffer{cap: maxOutput}
	stderr := &boundedBuffer{cap: maxOutput}
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	// Start the child, then apply rlimits to its PID via prlimit(2),
	// then Wait. Splitting Run() into Start+Wait is required because the
	// previous shell-wrapper approach was unsafe: it concatenated argv
	// into a /bin/sh -c string, which let any unquoted metacharacter in
	// an argument escape into shell scope. Now there is no shell at any
	// point in the chain.
	if err = cmd.Start(); err != nil {
		return &ToolResult{
			Content:  fmt.Sprintf("Failed to start tool: %v", err),
			IsError:  true,
			ExitCode: -1,
		}, nil
	}
	if tool.MaxCPUSeconds > 0 || tool.MaxMemoryMB > 0 || tool.MaxProcesses > 0 || tool.MaxFileSizeMB > 0 {
		if rerr := applyResourceLimits(cmd, tool); rerr != nil {
			logger.Warnf("Tool %q: applyResourceLimits failed: %v", tool.Name, rerr)
		}
	}
	err = cmd.Wait()

	output := stdout.buf.String() + stderr.buf.String()
	if stdout.truncated || stderr.truncated {
		output += "\n... (output truncated)"
	}

	// Ensure valid UTF-8
	if !utf8.ValidString(output) {
		output = strings.ToValidUTF8(output, "?")
	}

	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else if ctx.Err() == context.DeadlineExceeded {
			return &ToolResult{
				Content:  fmt.Sprintf("Tool execution timed out after %d seconds\n%s", timeout, output),
				IsError:  true,
				ExitCode: -1,
			}, nil
		} else {
			return &ToolResult{
				Content:  fmt.Sprintf("Failed to execute tool: %v\n%s", err, output),
				IsError:  true,
				ExitCode: -1,
			}, nil
		}
	}

	// Check if exit code is allowed
	isError := true
	allowedCodes := tool.AllowedExitCodes
	if len(allowedCodes) == 0 {
		allowedCodes = []int{0}
	}
	for _, code := range allowedCodes {
		if exitCode == code {
			isError = false
			break
		}
	}

	logger.Infof("Tool %q completed with exit code %d (error: %v)", tool.Name, exitCode, isError)

	// Capture output files
	var outputFiles []OutputFile
	for _, pf := range pendingFiles {
		data, readErr := os.ReadFile(pf.TempPath)
		if readErr != nil {
			logger.Warnf("Tool %q: could not read output file %s: %v", tool.Name, pf.TempPath, readErr)
			continue
		}
		// Removal is handled by the deferred cleanupTemps; no per-file cleanup here.

		filename := tool.Name + pf.Config.Extension
		outputFiles = append(outputFiles, OutputFile{
			Filename: filename,
			MimeType: pf.Config.MimeType,
			Data:     base64.StdEncoding.EncodeToString(data),
		})
		logger.Infof("Tool %q: captured output file %s (%d bytes)", tool.Name, filename, len(data))
	}

	return &ToolResult{
		Content:     output,
		IsError:     isError,
		ExitCode:    exitCode,
		OutputFiles: outputFiles,
	}, nil
}

// boundedBuffer accumulates at most cap bytes and discards the rest, so a tool
// that emits gigabytes cannot exhaust proxy memory before truncation. Write
// always reports the full length written so the child is never killed with a
// short-write / EPIPE.
type boundedBuffer struct {
	cap       int
	buf       bytes.Buffer
	truncated bool
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if rem := b.cap - b.buf.Len(); rem > 0 {
		if len(p) > rem {
			b.buf.Write(p[:rem])
			b.truncated = true
		} else {
			b.buf.Write(p)
		}
	} else if len(p) > 0 {
		b.truncated = true
	}
	return len(p), nil
}

// splitArgs splits a string into arguments, respecting single and double quotes.
func splitArgs(s string) []string {
	var args []string
	var current strings.Builder
	inSingle := false
	inDouble := false

	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch {
		case ch == '\'' && !inDouble:
			inSingle = !inSingle
		case ch == '"' && !inSingle:
			inDouble = !inDouble
		case ch == ' ' && !inSingle && !inDouble:
			if current.Len() > 0 {
				args = append(args, current.String())
				current.Reset()
			}
		default:
			current.WriteByte(ch)
		}
	}
	if current.Len() > 0 {
		args = append(args, current.String())
	}
	return args
}

// validExtension and validArgFlag enforce predictable shapes on the
// output_files config so a remote config push (config/update) cannot
// smuggle whitespace or shell metacharacters into argv composition.
var (
	extensionRe = regexp.MustCompile(`^\.[A-Za-z0-9]{1,16}$`)
	argFlagRe   = regexp.MustCompile(`^-{1,2}[A-Za-z0-9][A-Za-z0-9-]{0,15}$`)
)

func validExtension(s string) bool { return extensionRe.MatchString(s) }
func validArgFlag(s string) bool   { return argFlagRe.MatchString(s) }
