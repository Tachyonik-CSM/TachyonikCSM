// TachyonikProxy
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// The GMP built-ins: an OPENVAS SCAN appliance's host inventory, the results
// of its scan tasks' latest reports, which hosts it scans on a schedule and
// how often, and the test of the credential that reads them.
//
// They adapt package gmp to a tool: the appliance's address comes from the
// call's arguments, the GMP account from the credential linked to the tool
// installation, and the appliance's documents leave as output files that
// analysis and import rules turn into assets, vulnerabilities and detections.
// What is pinned is the appliance's SSH host key.

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
	"tachyonik/tachyonikproxy/internal/gmp/scancoverage"
)

// builtinGMPGetHosts is the command naming the OPENVAS SCAN host inventory.
const builtinGMPGetHosts = BuiltinPrefix + "gmp-get-hosts"

// builtinGMPGetResults is the command naming the results of each scan task's
// latest report.
const builtinGMPGetResults = BuiltinPrefix + "gmp-get-results"

// builtinGMPGetScanCoverage is the command naming the table of which targets
// the appliance scans on a schedule, and how often.
const builtinGMPGetScanCoverage = BuiltinPrefix + "gmp-get-scan-coverage"

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

// gmpGetResults fetches the results of every scan task's latest report and
// returns each report's document unmodified, one output file per task.
//
// One file per task rather than one combined: a combination would be a
// format of the proxy's own, where the appliance's get_results_response is
// one the platform's rules can rely on. Tasks that have never finished a run
// have no report and produce no file.
//
// max_output_bytes caps the files together, not each: the limit is on what
// one call sends back. Each response is read against what is left of it.
func gmpGetResults(tool *config.ToolConfig, args map[string]interface{}, secrets Secrets) (*ToolResult, error) {
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

	limit := tool.MaxOutputBytes
	if limit <= 0 {
		limit = defaultMaxOutputBytes
	}
	tooLarge := func() *ToolResult {
		return &ToolResult{
			Content: fmt.Sprintf("the latest scan results of %s are larger than this tool's max_output_bytes of %d — raise it in the tool rule",
				target.Host, limit),
			IsError: true,
		}
	}

	client.MaxResponseBytes = limit
	reports, err := client.LastReports()
	if err != nil {
		var big *gmp.ResponseTooLargeError
		if errors.As(err, &big) {
			return tooLarge(), nil
		}
		return &ToolResult{Content: describeGMPError(target, err), IsError: true}, nil
	}
	if len(reports) == 0 {
		return &ToolResult{Content: fmt.Sprintf("%s has no scan task with a finished report.", target.Host)}, nil
	}

	now := time.Now()
	var files []OutputFile
	used, total := 0, 0
	for _, r := range reports {
		client.MaxResponseBytes = limit - used
		if client.MaxResponseBytes <= 0 {
			return tooLarge(), nil
		}
		res, err := client.Results(r.ReportID)
		if err != nil {
			var big *gmp.ResponseTooLargeError
			if errors.As(err, &big) {
				return tooLarge(), nil
			}
			return &ToolResult{Content: fmt.Sprintf("task %q: %s", r.TaskName, describeGMPError(target, err)), IsError: true}, nil
		}
		used += len(res.Document)
		total += res.Count
		filename := gmpResultsFilename(target.Host, r.TaskID, now)
		logger.Infof("gmp-get-results: task %q report %s: %d result(s) from %s, %d bytes, as %s",
			r.TaskName, r.ReportID, res.Count, target.Host, len(res.Document), filename)
		files = append(files, OutputFile{
			Filename: filename,
			MimeType: "application/xml",
			Data:     base64.StdEncoding.EncodeToString(res.Document),
		})
	}

	return &ToolResult{
		Content: fmt.Sprintf("Retrieved %d results from the latest reports of %d scan tasks on %s (%d bytes) as %d files.",
			total, len(reports), target.Host, used, len(files)),
		OutputFiles: files,
	}, nil
}

// gmpGetScanCoverage works out which targets the appliance scans on a
// schedule, and the longest wait between two scans of each in days, and
// returns them as a "target|scan-interval" table (package scancoverage).
//
// The one GMP built-in whose file is not the appliance's own document: the
// answer is spread over the tasks, their targets and their schedules, and the
// table is what an import rule can apply to assets. A table without rows is
// still returned — that nothing is scanned on a schedule is an answer too.
func gmpGetScanCoverage(tool *config.ToolConfig, args map[string]interface{}, secrets Secrets) (*ToolResult, error) {
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

	limit := tool.MaxOutputBytes
	if limit <= 0 {
		limit = defaultMaxOutputBytes
	}
	// The limit is on the documents read here; the table is far smaller.
	client.MaxResponseBytes = limit
	fail := func(err error) *ToolResult {
		var big *gmp.ResponseTooLargeError
		if errors.As(err, &big) {
			return &ToolResult{
				Content: fmt.Sprintf("the %s answer of %s is larger than this tool's max_output_bytes of %d — raise it in the tool rule",
					big.Command, target.Host, limit),
				IsError: true,
			}
		}
		return &ToolResult{Content: describeGMPError(target, err), IsError: true}
	}
	tasks, err := client.ScanTasks()
	if err != nil {
		return fail(err), nil
	}
	targets, err := client.Targets()
	if err != nil {
		return fail(err), nil
	}

	now := time.Now()
	table, err := scancoverage.Build(tasks, targets, now)
	if err != nil {
		return &ToolResult{Content: fmt.Sprintf("could not work out the scan coverage of %s: %v", target.Host, err), IsError: true}, nil
	}
	for _, s := range table.Skipped {
		logger.Infof("gmp-get-scan-coverage: task %q left out: %s", s.Task, s.Reason)
	}
	doc := table.Render()
	filename := gmpScanCoverageFilename(target.Host, now)
	logger.Infof("gmp-get-scan-coverage: %d target(s) from %d scheduled task(s) on %s, %d bytes, as %s",
		len(table.Rows), table.Scheduled, target.Host, len(doc), filename)
	return &ToolResult{
		Content: fmt.Sprintf("Retrieved the scan coverage of %s: %d targets from %d scheduled scan tasks (%d tasks left out) as %s.",
			target.Host, len(table.Rows), table.Scheduled, len(table.Skipped), filename),
		OutputFiles: []OutputFile{{
			Filename: filename,
			MimeType: "text/plain",
			Data:     base64.StdEncoding.EncodeToString(doc),
		}},
	}, nil
}

// gmpScanCoverageFilename names the table after the appliance and the time it
// was worked out.
func gmpScanCoverageFilename(host string, at time.Time) string {
	return fmt.Sprintf("openvas-scan-coverage-%s-%s.txt", filenameSafe(host), at.UTC().Format("20060102T150405Z"))
}

// gmpResultsFilename names one task's results after the appliance, the task
// and the time they were fetched.
func gmpResultsFilename(host, taskID string, at time.Time) string {
	return fmt.Sprintf("openvas-results-%s-%s-%s.xml", filenameSafe(host), filenameSafe(taskID), at.UTC().Format("20060102T150405Z"))
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
	return fmt.Sprintf("could not read from %s: %v", target.Host, err)
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
