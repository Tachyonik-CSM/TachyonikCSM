// TachyonikProxy
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// The built-in tool TachyonikHAS (Host Asset Scanner): finds alive hosts in a
// private network (internal/hostscan), for a customer with no security tool that
// has the capability "Host detection".
//
// It takes the same "target" argument as a rule running Nmap, so what is written
// for any tool with that capability works with this one. The hosts come back as
// a JSON output file, which ToolManager stores as a source and the platform's
// analysis and import rules turn into assets. The file says plainly that it comes
// from TachyonikHAS and imitates no other tool's report.

package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"tachyonik/lib/logger"
	"tachyonik/tachyonikproxy/internal/config"
	"tachyonik/tachyonikproxy/internal/hostscan"
)

// builtinHASScan is the command naming the scan.
const builtinHASScan = BuiltinPrefix + "has-scan"

// hasDefaultTimeout applies when the tool rule sets no timeout.
const hasDefaultTimeout = 300 * time.Second

// hasRunning allows one scan at a time: a second, started by another rule or a
// chat, would double the load on the network for an answer on its way anyway.
var hasRunning sync.Mutex

// hasDocument is the output file. schemaVersion moves when a field changes
// meaning, since import rules are written against this shape.
type hasDocument struct {
	Tool            string          `json:"tool"`
	SchemaVersion   int             `json:"schemaVersion"`
	Target          string          `json:"target"`
	StartedAt       string          `json:"startedAt"`
	DurationMs      int64           `json:"durationMs"`
	AddressesProbed int             `json:"addressesProbed"`
	Complete        bool            `json:"complete"`
	ICMPUsed        bool            `json:"icmpUsed"`
	Warnings        []string        `json:"warnings"`
	Hosts           []hostscan.Host `json:"hosts"`
}

func hasScan(tool *config.ToolConfig, args map[string]interface{}, _ Secrets) (*ToolResult, error) {
	target, _ := args["target"].(string)
	target = strings.TrimSpace(target)
	if target == "" {
		return &ToolResult{Content: `argument "target" is required: an IP address, a CIDR range, or a comma-separated list of them`, IsError: true}, nil
	}
	addrs, err := hostscan.ParseTargets(target)
	if err != nil {
		return &ToolResult{Content: err.Error(), IsError: true}, nil
	}

	if !hasRunning.TryLock() {
		return &ToolResult{Content: "another TachyonikHAS scan is running on this proxy; try again when it has finished", IsError: true}, nil
	}
	defer hasRunning.Unlock()

	// Stops short of the rule's timeout, so what was found still gets back.
	timeout := time.Duration(tool.Timeout) * time.Second
	if timeout <= 0 {
		timeout = hasDefaultTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout*9/10)
	defer cancel()

	started := time.Now().UTC()
	res := hostscan.Scan(ctx, addrs)

	doc, err := json.MarshalIndent(hasDocument{
		Tool:            "TachyonikHAS",
		SchemaVersion:   1,
		Target:          target,
		StartedAt:       started.Format(time.RFC3339),
		DurationMs:      res.Duration.Milliseconds(),
		AddressesProbed: res.Addresses,
		Complete:        res.Complete,
		ICMPUsed:        res.ICMPUsed,
		Warnings:        nonNil(res.Warnings),
		Hosts:           res.Hosts,
	}, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode the scan result: %w", err)
	}

	limit := tool.MaxOutputBytes
	if limit <= 0 {
		limit = defaultMaxOutputBytes
	}
	if int64(len(doc)) > int64(limit) {
		return &ToolResult{
			Content: fmt.Sprintf("the scan found %s, more than this tool's max_output_bytes of %d holds — raise it in the tool rule", countOf(len(res.Hosts), "host"), limit),
			IsError: true,
		}, nil
	}

	filename := hasFilename(started)
	logger.Infof("has-scan: %d host(s) among %d address(es) in %s, complete=%v, icmp=%v, as %s",
		len(res.Hosts), res.Addresses, res.Duration.Round(time.Millisecond), res.Complete, res.ICMPUsed, filename)

	summary := fmt.Sprintf("Found %s among %s in %s, as %s.",
		countOf(len(res.Hosts), "alive host"), countOf(res.Addresses, "address"),
		res.Duration.Round(100*time.Millisecond), filename)
	for _, w := range res.Warnings {
		summary += " " + w
	}
	return &ToolResult{
		Content: summary,
		OutputFiles: []OutputFile{{
			Filename: filename,
			MimeType: "application/json",
			Data:     base64.StdEncoding.EncodeToString(doc),
		}},
	}, nil
}

func hasFilename(t time.Time) string {
	return "tachyonikhas-hosts-" + t.UTC().Format("20060102T150405Z") + ".json"
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
