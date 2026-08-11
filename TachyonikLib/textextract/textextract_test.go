// TachyonikLib
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests for text extraction: pulling text out of a PDF assembled in-test,
// passing non-PDF formats through untouched, media-type detection, the promise
// that a malformed PDF degrades to empty text instead of panicking, and the
// input and output bounds that stop a PDF bomb from exhausting memory.

package textextract

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

// minimalPDF builds a valid single-page PDF whose content stream draws the
// given (ASCII) text, computing correct xref byte offsets so a strict reader
// can parse it. Kept in the test so we validate real extraction without pulling
// in a PDF-writer dependency.
func minimalPDF(text string) []byte {
	var b bytes.Buffer
	var offsets []int
	obj := func(body string) {
		offsets = append(offsets, b.Len())
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", len(offsets), body)
	}

	b.WriteString("%PDF-1.4\n")
	content := fmt.Sprintf("BT /F1 24 Tf 72 700 Td (%s) Tj ET", text)
	obj("<< /Type /Catalog /Pages 2 0 R >>")
	obj("<< /Type /Pages /Kids [3 0 R] /Count 1 >>")
	obj("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >>")
	obj(fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content))
	obj("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding >>")

	xrefOff := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n", len(offsets)+1)
	b.WriteString("0000000000 65535 f \n")
	for _, off := range offsets {
		fmt.Fprintf(&b, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(offsets)+1, xrefOff)
	return b.Bytes()
}

func TestAnalyze_PDFExtractsText(t *testing.T) {
	const want = "OpenVAS Vulnerability Report"
	raw := minimalPDF(want)

	content, mime := Analyze(raw)
	if mime != MIMEPDF {
		t.Fatalf("mime = %q, want %q", mime, MIMEPDF)
	}
	if !strings.Contains(content, want) {
		t.Fatalf("extracted text %q does not contain %q", content, want)
	}
}

func TestAnalyze_NonPDFPassthrough(t *testing.T) {
	raw := []byte("<?xml version=\"1.0\"?><report id=\"x\">hello</report>")
	content, mime := Analyze(raw)
	if content != string(raw) {
		t.Fatalf("content changed for non-PDF: %q", content)
	}
	if mime == MIMEPDF {
		t.Fatalf("non-PDF detected as PDF")
	}
}

func TestAnalyze_MalformedPDFNoPanic(t *testing.T) {
	// Has the PDF magic but is otherwise garbage — must not panic, must yield
	// no text while still reporting the PDF media type.
	raw := []byte("%PDF-1.4\nthis is not a real pdf body\n%%EOF")
	content, mime := Analyze(raw)
	if mime != MIMEPDF {
		t.Fatalf("mime = %q, want %q", mime, MIMEPDF)
	}
	if content != "" {
		t.Fatalf("expected empty text for malformed PDF, got %q", content)
	}
}

func TestDetectMIME_PDF(t *testing.T) {
	if got := DetectMIME([]byte("%PDF-1.7\n...")); got != MIMEPDF {
		t.Fatalf("DetectMIME = %q, want %q", got, MIMEPDF)
	}
}

// A PDF larger than the input bound is never handed to the parser: extraction
// cost tracks what the compressed streams expand to, not the file size, so
// there is no bound left to apply once parsing starts.
func TestAnalyze_OversizedPDFNotParsed(t *testing.T) {
	raw := minimalPDF("OpenVAS Vulnerability Report")

	content, mime := analyze(raw, len(raw)-1, MaxTextBytes)
	if mime != MIMEPDF {
		t.Fatalf("mime = %q, want %q — the media type must survive the refusal", mime, MIMEPDF)
	}
	if content != "" {
		t.Fatalf("oversized PDF was parsed anyway, got %q", content)
	}

	// One byte of headroom and the same document extracts normally.
	if content, _ = analyze(raw, len(raw), MaxTextBytes); content == "" {
		t.Fatal("PDF exactly at the input limit should still be parsed")
	}
}

// A PDF bomb decompresses far beyond its file size. Extraction truncates at the
// text bound instead of growing the heap without limit.
func TestAnalyze_TextTruncatedAtLimit(t *testing.T) {
	const maxText = 8
	raw := minimalPDF("OpenVAS Vulnerability Report")

	full, _ := analyze(raw, MaxInputBytes, MaxTextBytes)
	if len(full) <= maxText {
		t.Fatalf("test needs text longer than %d bytes to be meaningful, got %d", maxText, len(full))
	}

	content, mime := analyze(raw, MaxInputBytes, maxText)
	if mime != MIMEPDF {
		t.Fatalf("mime = %q, want %q", mime, MIMEPDF)
	}
	if len(content) != maxText {
		t.Fatalf("len(content) = %d, want exactly %d", len(content), maxText)
	}
	// Truncated, not discarded: the leading text is still there to match on.
	if !strings.HasPrefix(full, content) {
		t.Fatalf("truncated text %q is not a prefix of the full text %q", content, full)
	}
}

// The bounds are PDF-specific; a text format is passed through whole.
func TestAnalyze_LimitsDoNotApplyToNonPDF(t *testing.T) {
	raw := []byte(strings.Repeat("a", 128))
	content, _ := analyze(raw, 8, 8)
	if content != string(raw) {
		t.Fatalf("non-PDF content was bounded: len = %d, want %d", len(content), len(raw))
	}
}
