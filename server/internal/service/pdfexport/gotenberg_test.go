package pdfexport

import (
	"context"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// gotenberg_test.go exercises the HTTP client against an httptest
// server that mimics gotenberg's contract. We don't actually pull
// the gotenberg image — these tests run anywhere Go is installed.
// Real end-to-end gotenberg integration lives in CI alongside the
// docker-compose service (lands with this PR; tested by booting
// the compose stack and curl-ing the endpoint).

const fakePDF = "%PDF-1.4\nfake pdf body\n%%EOF\n"

func TestRenderPDF_SuccessfulRoundtrip(t *testing.T) {
	t.Parallel()
	var receivedHTML string
	var receivedFields = map[string]string{}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method: got %q, want POST", r.Method)
		}
		if !strings.HasSuffix(r.URL.Path, "/forms/chromium/convert/html") {
			t.Errorf("path: got %q", r.URL.Path)
		}
		mediaType, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || mediaType != "multipart/form-data" {
			t.Fatalf("Content-Type: got %q, err=%v", r.Header.Get("Content-Type"), err)
		}
		mr := multipart.NewReader(r.Body, params["boundary"])
		for {
			part, err := mr.NextPart()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				t.Fatalf("NextPart: %v", err)
			}
			body, _ := io.ReadAll(part)
			switch part.FormName() {
			case "files":
				receivedHTML = string(body)
			default:
				receivedFields[part.FormName()] = string(body)
			}
		}
		w.Header().Set("Content-Type", "application/pdf")
		_, _ = w.Write([]byte(fakePDF))
	}))
	defer ts.Close()

	pdf, err := RenderPDF(context.Background(),
		GotenbergConfig{BaseURL: ts.URL, HTTPClient: ts.Client()},
		[]byte("<html><body>hello</body></html>"))
	if err != nil {
		t.Fatalf("RenderPDF: %v", err)
	}
	if string(pdf) != fakePDF {
		t.Errorf("pdf body: got %q", pdf)
	}
	if !strings.Contains(receivedHTML, "hello") {
		t.Errorf("server did not receive html body, got %q", receivedHTML)
	}
	// Sanity-check that the well-known form fields the gotenberg
	// API expects survive the multipart encoding. Any of these
	// going missing would silently corrupt page format / margins
	// in production, which is hard to notice without a visual
	// check.
	wantFields := []string{
		"paperWidth", "paperHeight",
		"marginTop", "marginBottom", "marginLeft", "marginRight",
		"emulatedMediaType",
	}
	for _, k := range wantFields {
		if _, ok := receivedFields[k]; !ok {
			t.Errorf("missing form field %q", k)
		}
	}
}

func TestRenderPDF_GotenbergReturnsNonPDFIsRenderError(t *testing.T) {
	t.Parallel()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("not a pdf"))
	}))
	defer ts.Close()

	_, err := RenderPDF(context.Background(),
		GotenbergConfig{BaseURL: ts.URL, HTTPClient: ts.Client()},
		[]byte("<html></html>"))
	if !errors.Is(err, ErrGotenbergRender) {
		t.Errorf("want ErrGotenbergRender, got %v", err)
	}
}

func TestRenderPDF_NonOKStatusIsRenderError(t *testing.T) {
	t.Parallel()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("chromium oom"))
	}))
	defer ts.Close()

	_, err := RenderPDF(context.Background(),
		GotenbergConfig{BaseURL: ts.URL, HTTPClient: ts.Client()},
		[]byte("<html></html>"))
	if !errors.Is(err, ErrGotenbergRender) {
		t.Errorf("want ErrGotenbergRender, got %v", err)
	}
	if !strings.Contains(err.Error(), "chromium oom") {
		t.Errorf("error should surface upstream message, got %v", err)
	}
}

func TestRenderPDF_ConnectionRefusedIsUnreachable(t *testing.T) {
	t.Parallel()
	// Use a port that nothing should be listening on.
	_, err := RenderPDF(context.Background(),
		GotenbergConfig{
			BaseURL:    "http://127.0.0.1:1", // /etc/services says "tcpmux"; closed by default
			HTTPClient: &http.Client{Timeout: 500 * time.Millisecond},
		},
		[]byte("<html></html>"))
	if !errors.Is(err, ErrGotenbergUnreachable) {
		t.Errorf("want ErrGotenbergUnreachable, got %v", err)
	}
}

func TestRenderPDF_EmptyHTMLRejected(t *testing.T) {
	t.Parallel()
	_, err := RenderPDF(context.Background(),
		GotenbergConfig{BaseURL: "http://does-not-matter"},
		nil)
	if err == nil {
		t.Fatal("expected error on empty HTML")
	}
}

func TestRenderPDF_EmptyBaseURLRejected(t *testing.T) {
	t.Parallel()
	_, err := RenderPDF(context.Background(),
		GotenbergConfig{BaseURL: ""},
		[]byte("<html></html>"))
	if err == nil {
		t.Fatal("expected error on empty BaseURL")
	}
}

func TestRenderPDF_OversizedHTMLRejected(t *testing.T) {
	t.Parallel()
	huge := make([]byte, MaxHTMLSize+1)
	_, err := RenderPDF(context.Background(),
		GotenbergConfig{BaseURL: "http://does-not-matter"},
		huge)
	if !errors.Is(err, ErrHTMLTooLarge) {
		t.Errorf("want ErrHTMLTooLarge, got %v", err)
	}
}
