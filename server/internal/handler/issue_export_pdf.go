package handler

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/multica-ai/multica/server/internal/service/pdfexport"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// ExportIssuePDF serves a PDF rendition of an issue (whole ticket or
// a single thread):
//
//  1. Load the document via pdfexport.LoadDocument (PR-1b).
//  2. Render it to HTML via pdfexport.RenderHTML (PR-1).
//  3. POST the HTML to the gotenberg sidecar via pdfexport.RenderPDF.
//  4. Return the PDF bytes with a predictable Content-Disposition.
//  5. Publish issue:exported on the event bus — activity_listeners.go
//     writes the audit row (finding 9A from /plan-eng-review).
//
// Query params:
//
//	thread=<comment-id>   optional; switches to ModeThread.
//
// Mounted unconditionally at /api/issues/{id}/export.pdf — see
// router.go. The dev HTML preview route (ExportIssueHTMLDev,
// PR-1b) remains, gated by MULTICA_DEV=1, for visual debugging.
func (h *Handler) ExportIssuePDF(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
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
	start := time.Now()

	doc, err := pdfexport.LoadDocument(ctx, h.Queries, issue, mode, threadRootID)
	if err != nil {
		switch {
		case errors.Is(err, pdfexport.ErrInvalidThreadRoot):
			writeError(w, http.StatusBadRequest, "invalid thread id")
		case errors.Is(err, pdfexport.ErrThreadRootNotFound):
			writeError(w, http.StatusNotFound, "thread not found")
		default:
			slog.Error("pdfexport: load failed",
				"issue_id", uuidToString(issue.ID), "err", err)
			writeError(w, http.StatusInternalServerError, "failed to load issue for export")
		}
		return
	}

	prefix := h.getIssuePrefix(ctx, issue.WorkspaceID)
	identifier := prefix + "-" + strconv.Itoa(int(issue.Number))
	doc.Header.Identifier = identifier
	doc.Header.URL = canonicalIssueURL(prefix, int(issue.Number))

	html, err := pdfexport.RenderHTML(ctx, doc)
	if err != nil {
		switch {
		case errors.Is(err, pdfexport.ErrHTMLTooLarge):
			// Finding 12A: huge tickets get a 413 with a hint to
			// use thread export.
			writeError(w, http.StatusRequestEntityTooLarge,
				"ticket too large for single PDF; try thread export")
		default:
			slog.Error("pdfexport: render failed",
				"issue_id", uuidToString(issue.ID), "err", err)
			writeError(w, http.StatusInternalServerError, "failed to render issue")
		}
		return
	}

	if h.cfg.GotenbergURL == "" {
		slog.Error("pdfexport: GOTENBERG_URL not configured")
		writeError(w, http.StatusServiceUnavailable,
			"PDF service not configured")
		return
	}
	pdf, err := pdfexport.RenderPDF(ctx,
		pdfexport.GotenbergConfig{BaseURL: h.cfg.GotenbergURL},
		html)
	if err != nil {
		switch {
		case errors.Is(err, pdfexport.ErrHTMLTooLarge):
			writeError(w, http.StatusRequestEntityTooLarge,
				"ticket too large for single PDF; try thread export")
		case errors.Is(err, pdfexport.ErrGotenbergUnreachable):
			slog.Error("pdfexport: gotenberg unreachable",
				"issue_id", uuidToString(issue.ID), "err", err)
			writeError(w, http.StatusServiceUnavailable,
				"PDF service unavailable; please retry")
		case errors.Is(err, pdfexport.ErrGotenbergRender):
			slog.Error("pdfexport: gotenberg render failed",
				"issue_id", uuidToString(issue.ID), "err", err)
			writeError(w, http.StatusBadGateway,
				"PDF render failed; please retry")
		default:
			slog.Error("pdfexport: unexpected RenderPDF error",
				"issue_id", uuidToString(issue.ID), "err", err)
			writeError(w, http.StatusInternalServerError, "failed to render PDF")
		}
		return
	}

	filename := buildExportFilename(identifier, mode, threadRootID)
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	w.Header().Set("Content-Length", strconv.Itoa(len(pdf)))
	w.Header().Set("Cache-Control", "private, no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(pdf)

	// Audit row via the existing activity-log bus (finding 9A).
	// Best-effort — failure to publish doesn't affect the response
	// the client already received. activity_listeners.go writes the
	// activity_log row when the event arrives.
	h.publishExportActivity(r, userID, issue, mode, threadRootID, len(pdf), time.Since(start))
}

func (h *Handler) publishExportActivity(
	r *http.Request, userID string, issue db.Issue,
	mode pdfexport.Mode, threadRootID string,
	bytes int, duration time.Duration,
) {
	if h.Bus == nil {
		return
	}
	workspaceID := uuidToString(issue.WorkspaceID)
	actorType, actorID := h.resolveActor(r, userID, workspaceID)

	modeStr := "full"
	if mode == pdfexport.ModeThread {
		modeStr = "thread"
	}
	h.publish(protocol.EventIssueExported, workspaceID, actorType, actorID,
		map[string]any{
			"issue_id":    uuidToString(issue.ID),
			"mode":        modeStr,
			"thread_id":   threadRootID,
			"bytes":       bytes,
			"duration_ms": duration.Milliseconds(),
		})
}

// buildExportFilename produces a deterministic, archival-friendly
// filename: <identifier>.pdf for full exports,
// <identifier>-thread-<sha8>.pdf for thread exports.
func buildExportFilename(identifier string, mode pdfexport.Mode, threadRootID string) string {
	if mode == pdfexport.ModeThread && threadRootID != "" {
		h := sha256.Sum256([]byte(threadRootID))
		return identifier + "-thread-" + hex.EncodeToString(h[:4]) + ".pdf"
	}
	return identifier + ".pdf"
}
