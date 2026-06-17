package render

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/chromedp"
)

// chromedpExec is the production Chrome driver. Imported by render.New as the
// default Exec. The single function takes the typed ExecRequest, drives a
// fresh chromedp browser context per call, and returns PNG bytes + a flag
// indicating whether a chart cell was present.
//
// Implementation choices:
//
//   - One Chrome instance per Screenshot call. The bot runs at most a handful
//     of agent comments per minute, so the ~500ms cold-start cost is fine.
//     Keeping browsers process-lifetime adds GC and crash-recovery edges
//     we don't need.
//   - chromedp.WaitVisible for the ready selector. WaitVisible polls every
//     10ms and respects ctx cancellation, so the deadline below caps total
//     wait time exactly.
//   - For the idle selector we use chromedp.WaitNotPresent — it returns once
//     zero matching nodes remain. Plan finding #2.
//   - chromedp.Screenshot scopes the capture to one selector — the renderer
//     produces only the cell rectangle, not the whole viewport.
func chromedpExec(ctx context.Context, req ExecRequest) (ExecResult, error) {
	// Deadline applies to the whole render attempt. Caller's context can
	// shorten this; the fixed ceiling protects us against runaway agents.
	ctx, cancel := context.WithTimeout(ctx, req.WaitTimeout)
	defer cancel()

	allocCtx, allocCancel := chromedp.NewExecAllocator(ctx,
		append(
			chromedp.DefaultExecAllocatorOptions[:],
			chromedp.Flag("headless", true),
			chromedp.Flag("disable-gpu", true),
			chromedp.Flag("no-sandbox", true),                          // running as systemd unit user, no setuid
			chromedp.WindowSize(1400, 900),
		)...,
	)
	defer allocCancel()

	browserCtx, browserCancel := chromedp.NewContext(allocCtx,
		chromedp.WithLogf(func(string, ...any) {}), // chromedp is chatty; route to /dev/null
	)
	defer browserCancel()

	var png []byte
	var nodes []*cdp.Node

	err := chromedp.Run(browserCtx,
		chromedp.Navigate(req.URL),
		chromedp.WaitVisible(req.ReadySelector, chromedp.ByQuery),
		// WaitNotPresent loops on the negative match — succeeds when all cells
		// have left the running/stale states.
		chromedp.ActionFunc(func(ctx context.Context) error {
			deadline, _ := ctx.Deadline()
			for {
				if err := ctx.Err(); err != nil {
					return err
				}
				var stillBusy []*cdp.Node
				if err := chromedp.Nodes(req.IdleSelector, &stillBusy, chromedp.ByQueryAll, chromedp.AtLeast(0)).Do(ctx); err != nil {
					return err
				}
				if len(stillBusy) == 0 {
					return nil
				}
				if !deadline.IsZero() && time.Now().After(deadline) {
					return ErrNotebookNotReady
				}
				select {
				case <-time.After(200 * time.Millisecond):
				case <-ctx.Done():
					return ctx.Err()
				}
			}
		}),
		// Check whether the chart cell exists before screenshotting. If no
		// matplotlib cell, return the "no chart" signal — caller posts text.
		chromedp.Nodes(req.CellSelector, &nodes, chromedp.ByQueryAll, chromedp.AtLeast(0)),
		chromedp.ActionFunc(func(ctx context.Context) error {
			if len(nodes) == 0 {
				return ErrNoChart
			}
			// Last node = newest cell, matching plan §"screenshot last cell".
			target := nodes[len(nodes)-1]
			selector := fmt.Sprintf(`document.querySelectorAll(%q)[%d]`, req.CellSelector, len(nodes)-1)
			_ = target // used implicitly through the query — kept for future scoped capture
			return chromedp.Screenshot(selector, &png, chromedp.ByJSPath).Do(ctx)
		}),
	)
	if errors.Is(err, ErrNoChart) {
		return ExecResult{HadChartCell: false}, nil
	}
	if errors.Is(err, ErrNotebookNotReady) {
		return ExecResult{}, ErrNotebookNotReady
	}
	if errors.Is(err, context.DeadlineExceeded) {
		slog.Default().Warn("mmbot/render: chrome deadline exceeded", "url", req.URL, "timeout", req.WaitTimeout)
		return ExecResult{}, ErrNotebookNotReady
	}
	if err != nil {
		return ExecResult{}, fmt.Errorf("chromedp.Run: %w", err)
	}
	return ExecResult{PNG: png, HadChartCell: true}, nil
}
