// SourceImporter
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// The ctx.csv binding: the fallback view, given to a routine when the source is
// neither XML nor JSON.
//
// The delimiter is detected from the content rather than declared, since an
// uploaded file carries no reliable statement of its own format, and rows of
// varying width are accepted rather than rejected — a report that pads or omits
// trailing fields is still worth importing. Beyond the raw rows, asRecords()
// offers the common case of keying each row by the header line.

package jsruntime

import (
	"encoding/csv"
	"io"
	"strings"

	"github.com/dop251/goja"
)

// DetectDelimiter picks the most likely delimiter ("," | "\t" | ";") from
// the first non-empty line of the content. Commas win ties. Returns "," on
// empty content as a safe default.
func DetectDelimiter(content string) rune {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		counts := map[rune]int{',': 0, '\t': 0, ';': 0}
		for _, r := range line {
			if _, ok := counts[r]; ok {
				counts[r]++
			}
		}
		// Prefer comma; otherwise whichever candidate appears most often.
		best := ','
		bestCount := counts[',']
		if counts['\t'] > bestCount {
			best = '\t'
			bestCount = counts['\t']
		}
		if counts[';'] > bestCount {
			best = ';'
		}
		return best
	}
	return ','
}

// ParseCSV parses content with the given delimiter using encoding/csv.
// Rows with varying field counts are accepted (FieldsPerRecord = -1).
// Quoting and embedded newlines are handled per RFC 4180.
func ParseCSV(content string, delimiter rune) [][]string {
	if content == "" {
		return nil
	}
	r := csv.NewReader(strings.NewReader(content))
	r.Comma = delimiter
	r.FieldsPerRecord = -1
	r.LazyQuotes = true
	r.ReuseRecord = false
	var rows [][]string
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			// Skip malformed rows but keep going; the routine can still use
			// what came before the first parse error.
			break
		}
		rows = append(rows, rec)
	}
	return rows
}

// BuildCSVBinding exposes ctx.csv with { rows, delimiter, asRecords() }.
// asRecords() returns rows[1..] as objects keyed by rows[0] (the header).
// Returns goja.Null when content is nil.
func BuildCSVBinding(vm *goja.Runtime, content string) goja.Value {
	delim := DetectDelimiter(content)
	rows := ParseCSV(content, delim)

	obj := vm.NewObject()

	// Exposing string delimiter rather than rune; matches JS ergonomics.
	_ = obj.Set("delimiter", string(delim))

	// Copy rows into interface{} slices so goja converts them into JS arrays.
	rowsVal := make([]interface{}, len(rows))
	for i, row := range rows {
		cells := make([]interface{}, len(row))
		for j, c := range row {
			cells[j] = c
		}
		rowsVal[i] = cells
	}
	_ = obj.Set("rows", rowsVal)

	_ = obj.Set("asRecords", func() goja.Value {
		if len(rows) < 2 {
			return vm.ToValue([]interface{}{})
		}
		headers := rows[0]
		records := make([]interface{}, 0, len(rows)-1)
		for i := 1; i < len(rows); i++ {
			rec := map[string]interface{}{}
			for j, h := range headers {
				if j < len(rows[i]) {
					rec[h] = rows[i][j]
				} else {
					rec[h] = ""
				}
			}
			records = append(records, rec)
		}
		return vm.ToValue(records)
	})

	return obj
}
