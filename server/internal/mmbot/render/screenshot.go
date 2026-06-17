// Package render takes screenshots of marimo notebook cells via a local
// headless Chrome process. The output is PNG bytes the bot then uploads to
// Mattermost as a file attachment, so Лина sees the chart inline in the
// thread (PUL-328 / plan finding #1).
//
// Two halves: a thin Renderer struct that owns config (target URL, selectors,
// timeouts) and a swappable `Exec` function that performs the actual Chrome
// drive. Production wires it to chromedpExec; tests inject a fake so package
// tests pass without a Chrome installation. A separate test file under build
// tag `chromedp` exercises the real Chrome path; CI sets the tag only on
// runners that have chromium provisioned.
//
// See: plans://Multica/2026-06-17-pul-328-mattermost-bot-marimo.md (revision 2).
package render

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// urlScan finds candidate http(s):// substrings inside a freeform comment
// body. We scan rather than split-on-spaces because markdown link syntax
// (`[label](url)`) puts the URL adjacent to a parenthesis with no space, and
// agents post both shapes interchangeably.
var urlScan = regexp.MustCompile(`https?://[^\s<>"'\)\]]+`)

// Sentinel errors so callers (outbound handler) can fall back to text-only
// posting when a screenshot is impossible without treating it as a hard
// failure.
var (
	// ErrEmptyNotebookFile means the comment we tried to extract a tailnet
	// URL from had no `?file=` query, or the file was blank.
	ErrEmptyNotebookFile = errors.New("mmbot/render: empty notebook file")

	// ErrNotebookNotReady means the marimo DOM never published
	// data-app-ready=true within the wait timeout.
	ErrNotebookNotReady = errors.New("mmbot/render: notebook not ready before timeout")

	// ErrNoChart means the notebook loaded but contained no matplotlib cell.
	// Caller should skip the screenshot step and post the comment as text.
	ErrNoChart = errors.New("mmbot/render: notebook has no matplotlib cell")

	// ErrChromeFailed wraps lower-level Chrome / chromedp failures. Caller
	// should fall back to text-only and log.
	ErrChromeFailed = errors.New("mmbot/render: headless chrome failed")
)

// Defaults documented here so config.go (Lane E) can override per-deployment.
const (
	DefaultMarimoURL    = "http://127.0.0.1:2718"
	DefaultWaitTimeout  = 30 * time.Second
	DefaultCellSelector = ".marimo-cell:has(.matplotlib-output)"
	DefaultReadySelector = "body[data-app-ready=\"true\"]"
	// IdleSelector is the negative match the renderer waits to NOT exist
	// (any cell that's still stale or running). Once zero such cells remain,
	// the notebook is settled and safe to screenshot. Finding #2 of
	// /plan-eng-review.
	IdleSelector = ".marimo-cell[data-cell-status=\"running\"], .marimo-cell[data-cell-status=\"stale\"]"
)

// ExecRequest carries everything the Chrome driver needs to take one screenshot.
type ExecRequest struct {
	URL              string
	ReadySelector    string
	IdleSelector     string
	CellSelector     string
	WaitTimeout      time.Duration
}

// ExecResult is what the driver hands back. PNG is empty when err != nil.
type ExecResult struct {
	PNG []byte
	// HadChartCell distinguishes "notebook ready but no chart" (no error,
	// empty PNG, caller posts text-only) from "could not drive chrome at
	// all" (error returned).
	HadChartCell bool
}

// Exec is the Chrome-driving function. Production uses chromedpExec; tests
// inject fakes. Keeping this as a function (not interface) means there's
// nothing to stub when you just want trivial happy-path tests.
type Exec func(ctx context.Context, req ExecRequest) (ExecResult, error)

// Config bundles construction options.
type Config struct {
	// MarimoBaseURL is the local marimo server, e.g. "http://127.0.0.1:2718".
	// Zero falls back to DefaultMarimoURL.
	MarimoBaseURL string
	// WaitTimeout caps how long we'll wait for the DOM to settle. Zero
	// falls back to DefaultWaitTimeout.
	WaitTimeout time.Duration
	// ReadySelector overrides the body[data-app-ready=true] selector. Zero
	// falls back to DefaultReadySelector.
	ReadySelector string
	// CellSelector overrides the matplotlib cell match. Zero falls back to
	// DefaultCellSelector.
	CellSelector string
	// IdleSelector overrides the "still running" negative-match. Zero falls
	// back to IdleSelector.
	IdleSelector string
	// Exec is the chrome driver. Zero falls back to chromedpExec.
	Exec Exec
}

// Renderer screenshots one marimo notebook cell on each Screenshot call.
type Renderer struct {
	cfg Config
}

// New constructs a Renderer with sensible defaults.
func New(cfg Config) *Renderer {
	if cfg.MarimoBaseURL == "" {
		cfg.MarimoBaseURL = DefaultMarimoURL
	}
	if cfg.WaitTimeout == 0 {
		cfg.WaitTimeout = DefaultWaitTimeout
	}
	if cfg.ReadySelector == "" {
		cfg.ReadySelector = DefaultReadySelector
	}
	if cfg.CellSelector == "" {
		cfg.CellSelector = DefaultCellSelector
	}
	if cfg.IdleSelector == "" {
		cfg.IdleSelector = IdleSelector
	}
	if cfg.Exec == nil {
		cfg.Exec = chromedpExec
	}
	return &Renderer{cfg: cfg}
}

// Screenshot returns PNG bytes for the last matplotlib cell in the given
// notebook file (e.g. "PUL-303.py"). Errors are typed; callers should treat
// ErrEmptyNotebookFile / ErrNoChart as "skip image, post text only" and
// ErrChromeFailed / ErrNotebookNotReady as "log and skip image."
func (r *Renderer) Screenshot(ctx context.Context, notebookFile string) ([]byte, error) {
	notebookFile = strings.TrimSpace(notebookFile)
	if notebookFile == "" {
		return nil, ErrEmptyNotebookFile
	}
	pageURL, err := r.BuildURL(notebookFile)
	if err != nil {
		return nil, err
	}

	req := ExecRequest{
		URL:           pageURL,
		ReadySelector: r.cfg.ReadySelector,
		IdleSelector:  r.cfg.IdleSelector,
		CellSelector:  r.cfg.CellSelector,
		WaitTimeout:   r.cfg.WaitTimeout,
	}
	res, err := r.cfg.Exec(ctx, req)
	if err != nil {
		// Already-wrapped sentinel errors flow through.
		if errors.Is(err, ErrNotebookNotReady) || errors.Is(err, ErrNoChart) || errors.Is(err, ErrEmptyNotebookFile) {
			return nil, err
		}
		return nil, fmt.Errorf("%w: %w", ErrChromeFailed, err)
	}
	if !res.HadChartCell {
		return nil, ErrNoChart
	}
	if len(res.PNG) == 0 {
		return nil, fmt.Errorf("%w: driver returned no PNG bytes", ErrChromeFailed)
	}
	return res.PNG, nil
}

// BuildURL renders the marimo viewer URL for a given notebook filename. The
// filename is properly escaped so that "PUL 328 notes.py" or files with
// "&" can't break the query string.
func (r *Renderer) BuildURL(notebookFile string) (string, error) {
	notebookFile = strings.TrimSpace(notebookFile)
	if notebookFile == "" {
		return "", ErrEmptyNotebookFile
	}
	// Defensive: callers may hand us a full path; we only want the base.
	notebookFile = filepath.Base(notebookFile)

	base, err := url.Parse(r.cfg.MarimoBaseURL)
	if err != nil {
		return "", fmt.Errorf("mmbot/render: parse marimo base URL: %w", err)
	}
	q := base.Query()
	q.Set("file", notebookFile)
	base.RawQuery = q.Encode()
	return base.String(), nil
}

// NotebookFromTailnetURL extracts the PUL-N.py basename from a tailnet URL
// embedded in an agent comment, e.g.
// "https://multica.tail38d0e3.ts.net:8443/?file=PUL-303.py".
//
// Returns ErrEmptyNotebookFile if the URL has no `file` query. Defensive
// against full paths in the query value (`file=/notebooks/PUL-303.py`).
func NotebookFromTailnetURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", ErrEmptyNotebookFile
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("mmbot/render: parse tailnet URL: %w", err)
	}
	file := strings.TrimSpace(parsed.Query().Get("file"))
	if file == "" {
		return "", ErrEmptyNotebookFile
	}
	file = filepath.Base(file)
	if file == "." || file == "/" {
		return "", ErrEmptyNotebookFile
	}
	return file, nil
}

// ExtractTailnetURL pulls the first tailnet marimo viewer URL out of a freeform
// comment body. Matches on the `?file=` query and a host that contains the
// configured tailnet hostname hint (default "ts.net"). Caller should treat
// the empty string return as "no screenshot to take, post text only."
//
// The hostname hint is **derived from MARIMO_LOCAL_URL_HOSTNAME** at
// configuration time rather than hardcoded — finding #1 of
// /plan-eng-review (so a tailnet rotation doesn't break detection).
//
// The URL scan handles markdown link shapes (`[label](url)`) and trailing
// punctuation (`.`, `,`, `;`) so it works on real agent comments.
func ExtractTailnetURL(body, hostHint string) string {
	if body == "" {
		return ""
	}
	hostHint = strings.TrimSpace(hostHint)
	if hostHint == "" {
		hostHint = "ts.net"
	}
	for _, candidate := range urlScan.FindAllString(body, -1) {
		stripped := strings.TrimRight(candidate, ".,;:!?")
		u, err := url.Parse(stripped)
		if err != nil {
			continue
		}
		if !strings.Contains(u.Host, hostHint) {
			continue
		}
		if u.Query().Get("file") == "" {
			continue
		}
		return stripped
	}
	return ""
}
