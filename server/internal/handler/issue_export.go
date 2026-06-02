package handler

import (
	"errors"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/multica-ai/multica/server/internal/service/pdfexport"
)

// PUL-266: hidden HTML preview route for the in-flight PDF export
// pipeline. Lands ahead of the gotenberg client (PR-2) so reviewers
// can verify the render visually in a browser — open
// /api/issues/{id}/export.html?_dev=1 against a workspace they're a
// member of and inspect the rendered timeline.
//
// The route is gated by the MULTICA_DEV environment variable: in
// production deployments MULTICA_DEV is unset and the route returns
// 404 indistinguishable from the not-found path. In local
// development and CI, MULTICA_DEV=1 unlocks the route. This keeps
// the surface area dev-only without a permanent feature flag — once
// PR-2 lands the gotenberg pipeline, the same handler will produce
// PDF (the only thing PR-2 changes is the response body and the
// path: /export.pdf with no env gate).

// ExportIssueHTMLDev serves the rendered HTML for an issue's PDF
// export. This is a development-only preview — the production PDF
// endpoint at /export.pdf lands in PR-2.
//
// Query params:
//
//	_dev=1               (required — together with MULTICA_DEV=1)
//	thread=<comment-id>  (optional — switches to ModeThread)
func (h *Handler) ExportIssueHTMLDev(w http.ResponseWriter, r *http.Request) {
	// Production safety: belt-and-braces over the MULTICA_DEV env
	// gate. APP_ENV=production overrides MULTICA_DEV unconditionally,
	// mirroring how MULTICA_DEV_VERIFICATION_CODE is neutered in
	// main.go. Both `_dev=1` and `MULTICA_DEV=1` must be set, and the
	// instance must not be flagged production. Any miss → 404,
	// indistinguishable from a real not-found.
	if strings.EqualFold(strings.TrimSpace(os.Getenv("APP_ENV")), "production") ||
		os.Getenv("MULTICA_DEV") != "1" ||
		r.URL.Query().Get("_dev") != "1" {
		writeError(w, http.StatusNotFound, "not found")
		return
	}

	id := chi.URLParam(r, "id")
	issue, ok := h.loadIssueForUser(w, r, id)
	if !ok {
		return
	}

	mode := pdfexport.ModeFull
	threadRootID := r.URL.Query().Get("thread")
	if threadRootID != "" {
		mode = pdfexport.ModeThread
	}

	ctx := r.Context()
	doc, err := pdfexport.LoadDocument(ctx, h.Queries, issue, mode, threadRootID)
	if err != nil {
		switch {
		case errors.Is(err, pdfexport.ErrInvalidThreadRoot):
			writeError(w, http.StatusBadRequest, "invalid thread id")
		case errors.Is(err, pdfexport.ErrThreadRootNotFound):
			writeError(w, http.StatusNotFound, "thread not found")
		default:
			writeError(w, http.StatusInternalServerError, "failed to load issue for export")
		}
		return
	}

	// Header gets the workspace's issue prefix (e.g. "PUL") so the
	// rendered identifier matches what users see in the UI ("PUL-266").
	prefix := h.getIssuePrefix(ctx, issue.WorkspaceID)
	doc.Header.Identifier = prefix + "-" + strconv.Itoa(int(issue.Number))
	doc.Header.URL = canonicalIssueURL(prefix, int(issue.Number))

	html, err := pdfexport.RenderHTML(ctx, doc)
	if err != nil {
		switch {
		case errors.Is(err, pdfexport.ErrHTMLTooLarge):
			// 413 Payload Too Large — finding 12A from the eng
			// review. The PDF endpoint (PR-2) returns the same shape.
			writeError(w, http.StatusRequestEntityTooLarge,
				"ticket too large for single PDF; try thread export")
		default:
			writeError(w, http.StatusInternalServerError, "failed to render issue")
		}
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store") // dev preview; don't poison browser cache
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(html)
}

// canonicalIssueURL builds the public multica URL for an issue from
// its identifier — used in the PDF footer so a printed export still
// has a clickable back-link.
//
// Mirrors the format the React UI uses (multica.ai/issues/<id>); we
// hard-code the host because (a) the API doesn't have a config knob
// for the canonical UI host and (b) the few places in the codebase
// that need this URL today also hard-code it.
func canonicalIssueURL(prefix string, number int) string {
	return "https://multica.ai/issues/" + prefix + "-" + strconv.Itoa(number)
}
