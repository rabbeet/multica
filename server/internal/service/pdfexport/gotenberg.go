package pdfexport

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// gotenberg.go ships the HTTP client that turns rendered HTML into
// PDF bytes. It talks to a Gotenberg sidecar — a small Chromium-
// backed service distributed as the docker.io/gotenberg/gotenberg
// image — over the Chromium HTML conversion endpoint.
//
// Why gotenberg and not embedded chromedp: in PUL-266
// /plan-eng-review we accepted finding 1A (gotenberg sidecar).
// Embedding Chromium directly into the API container would inflate
// the alpine runtime from ~50 MB to ~300 MB and tie API uptime to
// Chromium crashes. A separate gotenberg container isolates the
// failure domain (PDF rendering down → other API routes still up),
// keeps the API image small, and gives us a battle-tested HTML →
// PDF pipeline maintained by the gotenberg project. The reference
// PR description carries the full rationale.

// GotenbergConfig describes the upstream gotenberg endpoint and the
// timeouts we tolerate per request. Constructed once at server
// boot from environment variables (see server/cmd/server/main.go
// wiring) and passed in to every RenderPDF call.
type GotenbergConfig struct {
	// BaseURL is the gotenberg service root, e.g. "http://gotenberg:3000"
	// inside a docker compose network. Required.
	BaseURL string

	// HTTPClient defaults to a 60-second-timeout client if nil. Tests
	// inject an httptest server's client to keep the round-trip in
	// the test process.
	HTTPClient *http.Client
}

// DefaultGotenbergURL is the convention for the sidecar URL inside
// docker compose. Falls through to ${GOTENBERG_URL} when set on
// the server process.
const DefaultGotenbergURL = "http://gotenberg:3000"

// ErrGotenbergUnreachable surfaces network-level failures (DNS,
// connection refused, timeout). Handlers should map to HTTP 503 so
// the UI shows a "PDF service unavailable, retry?" toast.
var ErrGotenbergUnreachable = errors.New("pdfexport: gotenberg unreachable")

// ErrGotenbergRender surfaces gotenberg-side failures (a non-2xx
// response after a successful connection — typically a malformed
// HTML payload or a Chromium crash inside the sidecar). Handlers
// should map to HTTP 502: we reached the service but it couldn't
// produce a PDF.
var ErrGotenbergRender = errors.New("pdfexport: gotenberg render failed")

// RenderPDF posts rendered HTML to gotenberg's Chromium endpoint and
// returns the resulting PDF bytes. The HTML is wrapped in a
// multipart/form-data envelope as a file named "index.html" — that
// is the convention gotenberg expects per
// https://docs.gotenberg.dev/docs/routes#convert-html-files-into-pdf.
//
// Timeouts: respects ctx for cancellation. The HTTP client also
// carries its own deadline (default 60s) so a server-side
// goroutine leak in the renderer doesn't pin a chromium tab open
// forever in the sidecar. Both must fire — the strictest wins,
// which is correct.
//
// Note on streaming: gotenberg returns the full PDF as a blob
// (Chromium produces the document before returning), so there is
// no point pretending to stream chunks back. Our handler reads the
// entire response body into memory and writes it to the
// http.ResponseWriter; the client toast in the UI covers the
// time-to-first-byte. (Finding 5A from /plan-eng-review pinned
// this — we explicitly do NOT advertise chunked-transfer streaming
// for the export endpoint.)
func RenderPDF(ctx context.Context, cfg GotenbergConfig, html []byte) ([]byte, error) {
	if cfg.BaseURL == "" {
		return nil, fmt.Errorf("pdfexport: GotenbergConfig.BaseURL empty")
	}
	if len(html) == 0 {
		return nil, fmt.Errorf("pdfexport: RenderPDF called with empty html")
	}
	if len(html) > MaxHTMLSize {
		// Defense in depth: RenderHTML already checks this and
		// returns ErrHTMLTooLarge before we get here, but a
		// future caller that bypasses RenderHTML shouldn't be able
		// to OOM the sidecar.
		return nil, ErrHTMLTooLarge
	}

	endpoint, err := url.JoinPath(cfg.BaseURL, "/forms/chromium/convert/html")
	if err != nil {
		return nil, fmt.Errorf("pdfexport: build url: %w", err)
	}

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	part, err := writer.CreateFormFile("files", "index.html")
	if err != nil {
		return nil, fmt.Errorf("pdfexport: create form file: %w", err)
	}
	if _, err := part.Write(html); err != nil {
		return nil, fmt.Errorf("pdfexport: write form file: %w", err)
	}

	// Page format and margin tuning. A4 + 20mm matches the CSS in
	// assets/template.html and the make-pdf gstack conventions —
	// margins are advertised as inches because gotenberg's
	// Chromium PDF backend expects inches. 20mm ≈ 0.7874 in.
	formFields := map[string]string{
		"paperWidth":   "8.27",  // 210 mm in
		"paperHeight":  "11.69", // 297 mm in
		"marginTop":    "0.7874",
		"marginBottom": "0.7874",
		"marginLeft":   "0.7874",
		"marginRight":  "0.7874",
		"preferCSSPageSize": "true",
		// Default Chromium uses screen media; ours has only print
		// media queries, but staying on screen avoids surprises
		// when the renderer hasn't yet split @media rules.
		"emulatedMediaType": "screen",
	}
	for k, v := range formFields {
		if err := writer.WriteField(k, v); err != nil {
			return nil, fmt.Errorf("pdfexport: write form field %s: %w", k, err)
		}
	}
	if err := writer.Close(); err != nil {
		return nil, fmt.Errorf("pdfexport: close multipart writer: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, body)
	if err != nil {
		return nil, fmt.Errorf("pdfexport: build request: %w", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())

	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrGotenbergUnreachable, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode/100 != 2 {
		// gotenberg responds with text/plain error bodies; capture
		// up to 1 KiB to surface in logs / error messages without
		// flooding.
		preview := readPreview(resp.Body, 1024)
		return nil, fmt.Errorf("%w: status %d: %s",
			ErrGotenbergRender, resp.StatusCode, preview)
	}

	pdf, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("%w: read body: %v", ErrGotenbergRender, err)
	}
	if !bytes.HasPrefix(pdf, []byte("%PDF-")) {
		return nil, fmt.Errorf("%w: response not a PDF (first 8 bytes: %q)",
			ErrGotenbergRender, firstN(pdf, 8))
	}
	return pdf, nil
}

func readPreview(r io.Reader, n int64) string {
	b, _ := io.ReadAll(io.LimitReader(r, n))
	return strings.TrimSpace(string(b))
}

func firstN(b []byte, n int) []byte {
	if len(b) < n {
		return b
	}
	return b[:n]
}
