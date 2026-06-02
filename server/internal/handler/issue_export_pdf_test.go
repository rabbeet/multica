package handler

import (
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/service/pdfexport"
)

// TestBuildExportFilename pins the filename contract for the PR-266
// PDF export endpoint. The handler sets Content-Disposition from this
// helper; UI / CLI / iOS Files use the filename to keep multiple
// exports of the same ticket separable.
func TestBuildExportFilename(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name         string
		identifier   string
		mode         pdfexport.Mode
		threadRootID string
		wantPrefix   string
		wantSuffix   string
		wantSubstr   string
	}{
		{
			name:       "full ticket export",
			identifier: "PUL-266",
			mode:       pdfexport.ModeFull,
			wantPrefix: "PUL-266.pdf",
			wantSuffix: ".pdf",
		},
		{
			name:         "thread export uses 8-char sha8",
			identifier:   "PUL-266",
			mode:         pdfexport.ModeThread,
			threadRootID: "d7d12ffd-f19a-4507-841a-4c69d1a6808f",
			wantPrefix:   "PUL-266-thread-",
			wantSuffix:   ".pdf",
			wantSubstr:   "-thread-",
		},
		{
			name:         "thread mode with empty root falls back to full filename",
			identifier:   "PUL-266",
			mode:         pdfexport.ModeThread,
			threadRootID: "",
			wantPrefix:   "PUL-266.pdf",
			wantSuffix:   ".pdf",
		},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			got := buildExportFilename(c.identifier, c.mode, c.threadRootID)
			if !strings.HasPrefix(got, c.wantPrefix) {
				t.Errorf("filename %q does not start with %q", got, c.wantPrefix)
			}
			if !strings.HasSuffix(got, c.wantSuffix) {
				t.Errorf("filename %q does not end with %q", got, c.wantSuffix)
			}
			if c.wantSubstr != "" && !strings.Contains(got, c.wantSubstr) {
				t.Errorf("filename %q missing substring %q", got, c.wantSubstr)
			}
		})
	}
}

// TestBuildExportFilename_ThreadShaIsDeterministic verifies that the
// short-sha is stable across calls for the same thread id. Without
// determinism, a user re-exporting the same thread would get
// different filenames each time and archival/dedup workflows would
// break.
func TestBuildExportFilename_ThreadShaIsDeterministic(t *testing.T) {
	t.Parallel()
	const id = "d7d12ffd-f19a-4507-841a-4c69d1a6808f"
	first := buildExportFilename("PUL-266", pdfexport.ModeThread, id)
	for i := 0; i < 10; i++ {
		if again := buildExportFilename("PUL-266", pdfexport.ModeThread, id); again != first {
			t.Fatalf("filename not deterministic: %q vs %q", first, again)
		}
	}
}

// TestBuildExportFilename_DifferentThreadsDifferentNames verifies the
// short-sha actually disambiguates — a hash collision in 4 bytes is
// rare but possible, so we assert by construction (different inputs
// yield different outputs for these two reasonable IDs).
func TestBuildExportFilename_DifferentThreadsDifferentNames(t *testing.T) {
	t.Parallel()
	a := buildExportFilename("PUL-266", pdfexport.ModeThread,
		"d7d12ffd-f19a-4507-841a-4c69d1a6808f")
	b := buildExportFilename("PUL-266", pdfexport.ModeThread,
		"c622ff3d-403a-44df-900e-ba3f6e826734")
	if a == b {
		t.Errorf("two different thread roots produced the same filename: %q", a)
	}
}
