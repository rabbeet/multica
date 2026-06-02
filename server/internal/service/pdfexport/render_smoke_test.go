package pdfexport

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestRenderHTML_Smoke is a single sanity-check that exercises the full
// render pipeline (Markdown → goldmark → bluemonday → template) and
// verifies the obvious shape of the output. The richer corpus lives
// in render_test.go.
//
// This test must always run first and must always pass — if the pipeline
// can't even produce HTML for a 2-comment fixture, every other test in
// the package will fail in confusing ways.
func TestRenderHTML_Smoke(t *testing.T) {
	t.Parallel()
	createdAt := time.Date(2026, 6, 2, 5, 30, 0, 0, time.UTC)
	doc := Document{
		Mode: ModeFull,
		Header: TicketHeader{
			Identifier:   "PUL-266",
			Title:        "Экспорт в PDF",
			ProjectTitle: "Multica",
			Status:       "in_progress",
			Priority:     "none",
			CreatorName:  "Vadim",
			CreatedAt:    createdAt,
			UpdatedAt:    createdAt.Add(15 * time.Minute),
			URL:          "https://multica.ai/issues/PUL-266",
		},
		Description: "Хочу **экспорт** в PDF.",
		Items: []TimelineItem{
			CommentItem{
				ID:        "comment-1",
				ActorName: "Vadim",
				CreatedAt: createdAt.Add(5 * time.Minute),
				UpdatedAt: createdAt.Add(5 * time.Minute),
				Body:      "1. как ты предложил\n2. скачивание",
			},
			ActivityItem{
				ID:        "activity-1",
				ActorName: "Vadim",
				CreatedAt: createdAt.Add(10 * time.Minute),
				Action:    "changed status: todo → in_progress",
			},
		},
	}

	out, err := RenderHTML(context.Background(), doc)
	if err != nil {
		t.Fatalf("RenderHTML returned error: %v", err)
	}
	got := string(out)

	mustContain := []string{
		"<!DOCTYPE html>",
		"PUL-266",
		"Экспорт в PDF",
		"Vadim",
		`<strong>экспорт</strong>`, // goldmark turns **экспорт** into <strong>
		"скачивание",
		"changed status: todo → in_progress",
		"comment-block",
		"activity-row",
	}
	for _, want := range mustContain {
		if !strings.Contains(got, want) {
			t.Errorf("rendered HTML is missing %q\nfull output:\n%s", want, got)
		}
	}
}
