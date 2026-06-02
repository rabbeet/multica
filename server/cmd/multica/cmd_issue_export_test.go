package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// TestRunIssueExport_WritesFileFromDefaultFilename covers the happy
// path: server responds with PDF bytes + Content-Disposition, CLI
// writes the suggested filename to cwd and prints the absolute path.
func TestRunIssueExport_WritesFileFromDefaultFilename(t *testing.T) {
	const fakePDF = "%PDF-1.4\nfake\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/issues/PUL-266/export.pdf" {
			t.Errorf("path: got %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/pdf")
		w.Header().Set("Content-Disposition", `attachment; filename="PUL-266.pdf"`)
		_, _ = w.Write([]byte(fakePDF))
	}))
	defer srv.Close()

	t.Setenv("MULTICA_SERVER_URL", srv.URL)

	dir := t.TempDir()
	cwd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(cwd)

	cmd := &cobra.Command{}
	issueExportCmd.SetArgs([]string{"PUL-266"})
	registerExportFlags(cmd)
	cmd.SetArgs([]string{"PUL-266"})

	if err := runIssueExport(cmd, []string{"PUL-266"}); err != nil {
		t.Fatalf("runIssueExport: %v", err)
	}

	dest := filepath.Join(dir, "PUL-266.pdf")
	body, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("read written file: %v", err)
	}
	if string(body) != fakePDF {
		t.Errorf("file body: got %q, want %q", body, fakePDF)
	}
}

// TestRunIssueExport_ThreadFlagBuildsThreadQuery confirms ?thread=
// is forwarded verbatim.
func TestRunIssueExport_ThreadFlagBuildsThreadQuery(t *testing.T) {
	const fakePDF = "%PDF-1.4\nthreaded\n"
	var seenQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/pdf")
		w.Header().Set("Content-Disposition", `attachment; filename="PUL-266-thread-deadbeef.pdf"`)
		_, _ = w.Write([]byte(fakePDF))
	}))
	defer srv.Close()

	t.Setenv("MULTICA_SERVER_URL", srv.URL)

	dir := t.TempDir()
	cwd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(cwd)

	cmd := &cobra.Command{}
	registerExportFlags(cmd)
	if err := cmd.Flags().Set("thread", "d7d12ffd-f19a-4507-841a-4c69d1a6808f"); err != nil {
		t.Fatalf("set --thread: %v", err)
	}

	if err := runIssueExport(cmd, []string{"PUL-266"}); err != nil {
		t.Fatalf("runIssueExport: %v", err)
	}
	if !strings.Contains(seenQuery, "thread=d7d12ffd-f19a-4507-841a-4c69d1a6808f") {
		t.Errorf("server did not see thread query: %q", seenQuery)
	}
}

// TestRunIssueExport_CollisionWithoutForce verifies the -f gate: an
// existing destination file must not be silently overwritten without
// --force, matching finding 8A from /plan-eng-review.
func TestRunIssueExport_CollisionWithoutForce(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/pdf")
		w.Header().Set("Content-Disposition", `attachment; filename="PUL-266.pdf"`)
		_, _ = w.Write([]byte("%PDF-1.4\n"))
	}))
	defer srv.Close()
	t.Setenv("MULTICA_SERVER_URL", srv.URL)

	dir := t.TempDir()
	existing := filepath.Join(dir, "PUL-266.pdf")
	if err := os.WriteFile(existing, []byte("PRE-EXISTING"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	cmd := &cobra.Command{}
	registerExportFlags(cmd)
	if err := cmd.Flags().Set("output", existing); err != nil {
		t.Fatalf("set --output: %v", err)
	}

	err := runIssueExport(cmd, []string{"PUL-266"})
	if err == nil {
		t.Fatal("expected error on existing file without --force")
	}
	if !strings.Contains(err.Error(), "file exists") {
		t.Errorf("error message: got %q", err)
	}

	body, _ := os.ReadFile(existing)
	if string(body) != "PRE-EXISTING" {
		t.Errorf("file was overwritten: got %q", body)
	}
}

// TestRunIssueExport_ForceOverwrites confirms -f permits overwrite.
func TestRunIssueExport_ForceOverwrites(t *testing.T) {
	const fakePDF = "%PDF-1.4\nnew bytes\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/pdf")
		w.Header().Set("Content-Disposition", `attachment; filename="PUL-266.pdf"`)
		_, _ = w.Write([]byte(fakePDF))
	}))
	defer srv.Close()
	t.Setenv("MULTICA_SERVER_URL", srv.URL)

	dir := t.TempDir()
	existing := filepath.Join(dir, "PUL-266.pdf")
	if err := os.WriteFile(existing, []byte("OLD"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	cmd := &cobra.Command{}
	registerExportFlags(cmd)
	if err := cmd.Flags().Set("output", existing); err != nil {
		t.Fatalf("set --output: %v", err)
	}
	if err := cmd.Flags().Set("force", "true"); err != nil {
		t.Fatalf("set --force: %v", err)
	}

	if err := runIssueExport(cmd, []string{"PUL-266"}); err != nil {
		t.Fatalf("runIssueExport: %v", err)
	}
	body, _ := os.ReadFile(existing)
	if string(body) != fakePDF {
		t.Errorf("file was not overwritten with new bytes; got %q", body)
	}
}

// TestRunIssueExport_StdoutWritesPDFBytes verifies the -o "-" path
// writes the PDF blob to stdout (the only path that doesn't go
// through a file).
func TestRunIssueExport_StdoutWritesPDFBytes(t *testing.T) {
	const fakePDF = "%PDF-1.4\nstdout body\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/pdf")
		_, _ = w.Write([]byte(fakePDF))
	}))
	defer srv.Close()
	t.Setenv("MULTICA_SERVER_URL", srv.URL)

	cmd := &cobra.Command{}
	registerExportFlags(cmd)
	if err := cmd.Flags().Set("output", "-"); err != nil {
		t.Fatalf("set --output -: %v", err)
	}

	// Capture stdout via os.Pipe.
	origStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	defer func() { os.Stdout = origStdout }()

	errCh := make(chan error, 1)
	go func() {
		errCh <- runIssueExport(cmd, []string{"PUL-266"})
		_ = w.Close()
	}()

	out := make([]byte, 1024)
	n, _ := r.Read(out)
	if err := <-errCh; err != nil {
		t.Fatalf("runIssueExport: %v", err)
	}
	if !strings.Contains(string(out[:n]), "stdout body") {
		t.Errorf("stdout: got %q", out[:n])
	}
}

// registerExportFlags mirrors the cobra registration in init() so
// individual tests can build a fresh *cobra.Command and exercise
// the flags without invoking the whole Execute() pipeline.
func registerExportFlags(cmd *cobra.Command) {
	cmd.Flags().String("thread", "", "")
	cmd.Flags().StringP("output", "o", "", "")
	cmd.Flags().BoolP("force", "f", false, "")
	cmd.Flags().String("server-url", "", "")
	cmd.Flags().String("token", "", "")
}
