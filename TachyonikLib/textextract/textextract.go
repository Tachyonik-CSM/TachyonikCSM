// TachyonikLib
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package textextract turns a raw uploaded file into the text that analysis and
// import routines match against. For most formats (XML, JSON, CSV, plain text)
// the bytes are already text and are returned as-is. PDFs are the exception:
// their text lives in compressed content streams, so it is extracted here and
// returned as plain text, letting the same string-matching routines work on
// PDFs as on any text file.
//
// Extraction is best-effort and never fatal: an encrypted, scanned/image-only,
// or malformed PDF yields empty text (and the caller's routines simply find no
// match), never a panic or crash.
//
// Both ends of the extraction are bounded, because the input is a file someone
// uploaded. A PDF's text lives in Flate-compressed streams, so a small file can
// decompress to an arbitrarily large one; MaxInputBytes rejects oversized input
// up front and MaxTextBytes caps what a single document may expand to. Without
// them a "PDF bomb" exhausts memory, and the recover below cannot help — Go
// cannot recover from an out-of-memory condition.
package textextract

import (
	"bytes"
	"fmt"
	"io"
	"net/http"

	"github.com/ledongthuc/pdf"
	"tachyonik/lib/logger"
)

// MIMEPDF is the media type reported for PDF input.
const MIMEPDF = "application/pdf"

const (
	// MaxInputBytes is the largest PDF this package will attempt to parse.
	// Larger input is reported as a PDF with no text rather than parsed.
	MaxInputBytes = 64 << 20 // 64 MiB

	// MaxTextBytes caps the text a single PDF may expand to. A document whose
	// text layer exceeds it is truncated at the limit, not discarded: the
	// caller's rules still get the leading MaxTextBytes to match against.
	MaxTextBytes = 32 << 20 // 32 MiB
)

// Analyze detects the media type of raw and returns the text a rule should
// match against together with that media type. For PDFs the returned content is
// the extracted text (empty if no text layer could be read); for every other
// type the raw bytes are returned unchanged as a string.
func Analyze(raw []byte) (content string, mimeType string) {
	return analyze(raw, MaxInputBytes, MaxTextBytes)
}

// analyze is Analyze with the bounds passed in, so tests can exercise the
// limit paths without allocating the production-sized buffers.
func analyze(raw []byte, maxInput, maxText int) (content string, mimeType string) {
	mimeType = DetectMIME(raw)
	if mimeType == MIMEPDF {
		if len(raw) > maxInput {
			// Refusing to parse is the safe answer: the cost of extraction is
			// driven by what the streams decompress to, not by the file size,
			// so there is no bound to fall back on once parsing has started.
			logger.Warnf("textextract: PDF of %d bytes exceeds the %d byte parse limit — treating it as having no text", len(raw), maxInput)
			return "", mimeType
		}
		text, err := extractPDFText(raw, maxText)
		if err != nil {
			// Encrypted / scanned-image / malformed PDF: no text to match on.
			// Keep the PDF media type so a rule can still assert "it's a PDF".
			return "", mimeType
		}
		return text, mimeType
	}
	return string(raw), mimeType
}

// DetectMIME returns the media type of raw. PDFs are recognised by their magic
// prefix; everything else is sniffed with the standard library's content
// detector (which falls back to application/octet-stream).
func DetectMIME(raw []byte) string {
	if bytes.HasPrefix(raw, []byte("%PDF-")) {
		return MIMEPDF
	}
	n := len(raw)
	if n > 512 {
		n = 512
	}
	return http.DetectContentType(raw[:n])
}

// extractPDFText reads at most maxText bytes of a PDF's plain text.
// ledongthuc/pdf can panic on malformed input, so extraction is wrapped in a
// recover — a bad PDF must degrade to "no text", never crash the daemon.
func extractPDFText(raw []byte, maxText int) (text string, err error) {
	defer func() {
		if r := recover(); r != nil {
			text = ""
			err = fmt.Errorf("pdf extraction panicked: %v", r)
		}
	}()

	r, err := pdf.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return "", fmt.Errorf("open pdf: %w", err)
	}
	pt, err := r.GetPlainText()
	if err != nil {
		return "", fmt.Errorf("extract pdf text: %w", err)
	}
	// Read one byte past the cap so an exactly-at-limit document is not
	// mistaken for a truncated one.
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(io.LimitReader(pt, int64(maxText)+1)); err != nil {
		return "", fmt.Errorf("read pdf text: %w", err)
	}
	if buf.Len() > maxText {
		logger.Warnf("textextract: PDF text exceeds the %d byte limit — truncating; rules match against the leading portion only", maxText)
		return string(buf.Bytes()[:maxText]), nil
	}
	return buf.String(), nil
}
