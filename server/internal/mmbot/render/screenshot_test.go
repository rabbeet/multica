package render

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestExtractTailnetURL_FindsCanonicalForm(t *testing.T) {
	cases := []struct {
		name     string
		body     string
		hostHint string
		want     string
	}{
		{
			name:     "bare tailnet URL with file=",
			body:     "Done — see https://multica.tail38d0e3.ts.net:8443/?file=PUL-303.py",
			hostHint: "ts.net",
			want:     "https://multica.tail38d0e3.ts.net:8443/?file=PUL-303.py",
		},
		{
			name:     "markdown link with parentheses stripped",
			body:     "[notebook](https://multica.tail38d0e3.ts.net:8443/?file=PUL-328.py).",
			hostHint: "ts.net",
			want:     "https://multica.tail38d0e3.ts.net:8443/?file=PUL-328.py",
		},
		{
			name:     "multiple URLs — picks the tailnet one",
			body:     "https://github.com/foo and https://multica.tail38d0e3.ts.net/?file=X.py and more text",
			hostHint: "ts.net",
			want:     "https://multica.tail38d0e3.ts.net/?file=X.py",
		},
		{
			name:     "custom host hint overrides default",
			body:     "https://multica.internal.example.org/?file=Y.py",
			hostHint: "example.org",
			want:     "https://multica.internal.example.org/?file=Y.py",
		},
		{
			name:     "no URL → empty",
			body:     "just text no URL here",
			hostHint: "ts.net",
			want:     "",
		},
		{
			name:     "URL without file param → ignored",
			body:     "https://multica.tail38d0e3.ts.net:8443/dashboard",
			hostHint: "ts.net",
			want:     "",
		},
		{
			name:     "wrong host → ignored",
			body:     "https://github.com/?file=PUL-X.py",
			hostHint: "ts.net",
			want:     "",
		},
		{
			name:     "empty body",
			body:     "",
			hostHint: "ts.net",
			want:     "",
		},
		{
			name:     "empty host hint falls back to ts.net default",
			body:     "https://x.ts.net/?file=Z.py",
			hostHint: "",
			want:     "https://x.ts.net/?file=Z.py",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ExtractTailnetURL(c.body, c.hostHint)
			if got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestNotebookFromTailnetURL(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr error
	}{
		{"https://multica.tail38d0e3.ts.net:8443/?file=PUL-303.py", "PUL-303.py", nil},
		{"https://multica.tail38d0e3.ts.net/?file=/srv/marimo-notebooks/PUL-303.py", "PUL-303.py", nil},
		{"https://multica.tail38d0e3.ts.net/?file=  ", "", ErrEmptyNotebookFile},
		{"https://multica.tail38d0e3.ts.net/?other=x", "", ErrEmptyNotebookFile},
		{"", "", ErrEmptyNotebookFile},
		// Path-style "file=." should be treated as missing.
		{"https://x/?file=.", "", ErrEmptyNotebookFile},
	}
	for _, c := range cases {
		got, err := NotebookFromTailnetURL(c.in)
		if c.wantErr != nil {
			if !errors.Is(err, c.wantErr) {
				t.Errorf("[%s] err = %v, want %v", c.in, err, c.wantErr)
			}
			continue
		}
		if err != nil {
			t.Errorf("[%s] unexpected err: %v", c.in, err)
		}
		if got != c.want {
			t.Errorf("[%s] got %q, want %q", c.in, got, c.want)
		}
	}
}

func TestRenderer_BuildURL_AppendsFileQuery(t *testing.T) {
	r := New(Config{MarimoBaseURL: "http://127.0.0.1:2718", Exec: stubExec(t, ExecResult{PNG: []byte("png"), HadChartCell: true}, nil)})

	got, err := r.BuildURL("PUL-303.py")
	if err != nil {
		t.Fatalf("buildURL: %v", err)
	}
	want := "http://127.0.0.1:2718?file=PUL-303.py"
	if got != want {
		t.Errorf("URL = %q, want %q", got, want)
	}
}

func TestRenderer_BuildURL_EscapesSpaces(t *testing.T) {
	r := New(Config{Exec: stubExec(t, ExecResult{HadChartCell: true, PNG: []byte("png")}, nil)})
	got, err := r.BuildURL("My Notebook.py")
	if err != nil {
		t.Fatalf("buildURL: %v", err)
	}
	// url.Values encodes space as `+`. Either form is valid; we just want
	// to be sure we didn't emit a raw space.
	if got == "" || got == DefaultMarimoURL+"?file=My Notebook.py" {
		t.Errorf("URL not escaped: %q", got)
	}
}

func TestRenderer_BuildURL_StripsLeadingPath(t *testing.T) {
	r := New(Config{Exec: stubExec(t, ExecResult{HadChartCell: true, PNG: []byte("png")}, nil)})
	got, err := r.BuildURL("/srv/marimo-notebooks/PUL-303.py")
	if err != nil {
		t.Fatalf("buildURL: %v", err)
	}
	if got != DefaultMarimoURL+"?file=PUL-303.py" {
		t.Errorf("URL = %q, want default+?file=PUL-303.py", got)
	}
}

func TestRenderer_BuildURL_EmptyFails(t *testing.T) {
	r := New(Config{})
	_, err := r.BuildURL("   ")
	if !errors.Is(err, ErrEmptyNotebookFile) {
		t.Errorf("err = %v, want ErrEmptyNotebookFile", err)
	}
}

func TestRenderer_Screenshot_HappyPath(t *testing.T) {
	r := New(Config{
		MarimoBaseURL: "http://127.0.0.1:2718",
		Exec: func(ctx context.Context, req ExecRequest) (ExecResult, error) {
			if req.URL != "http://127.0.0.1:2718?file=PUL-303.py" {
				t.Errorf("URL passed to exec = %q", req.URL)
			}
			if req.CellSelector != DefaultCellSelector {
				t.Errorf("CellSelector = %q", req.CellSelector)
			}
			if req.ReadySelector != DefaultReadySelector {
				t.Errorf("ReadySelector = %q", req.ReadySelector)
			}
			if req.WaitTimeout != DefaultWaitTimeout {
				t.Errorf("WaitTimeout = %v", req.WaitTimeout)
			}
			return ExecResult{PNG: []byte{0x89, 0x50, 0x4e, 0x47}, HadChartCell: true}, nil
		},
	})
	png, err := r.Screenshot(context.Background(), "PUL-303.py")
	if err != nil {
		t.Fatalf("screenshot: %v", err)
	}
	if len(png) != 4 || png[0] != 0x89 {
		t.Errorf("PNG header lost: %v", png)
	}
}

func TestRenderer_Screenshot_NoChartIsErrNoChart(t *testing.T) {
	r := New(Config{
		Exec: stubExec(t, ExecResult{HadChartCell: false}, nil),
	})
	_, err := r.Screenshot(context.Background(), "PUL-303.py")
	if !errors.Is(err, ErrNoChart) {
		t.Errorf("err = %v, want ErrNoChart", err)
	}
}

func TestRenderer_Screenshot_NotebookNotReadyPasses(t *testing.T) {
	r := New(Config{
		Exec: stubExec(t, ExecResult{}, ErrNotebookNotReady),
	})
	_, err := r.Screenshot(context.Background(), "PUL-303.py")
	if !errors.Is(err, ErrNotebookNotReady) {
		t.Errorf("err = %v, want ErrNotebookNotReady", err)
	}
}

func TestRenderer_Screenshot_ChromeFailureWrapped(t *testing.T) {
	cause := errors.New("dial unix: connection refused")
	r := New(Config{
		Exec: stubExec(t, ExecResult{}, cause),
	})
	_, err := r.Screenshot(context.Background(), "PUL-303.py")
	if !errors.Is(err, ErrChromeFailed) {
		t.Errorf("err = %v, want ErrChromeFailed wrapper", err)
	}
	if !errors.Is(err, cause) {
		t.Errorf("err = %v, expected to wrap cause %v", err, cause)
	}
}

func TestRenderer_Screenshot_EmptyPNGIsChromeFailure(t *testing.T) {
	r := New(Config{
		Exec: stubExec(t, ExecResult{HadChartCell: true, PNG: nil}, nil),
	})
	_, err := r.Screenshot(context.Background(), "PUL-303.py")
	if !errors.Is(err, ErrChromeFailed) {
		t.Errorf("err = %v, want ErrChromeFailed (driver returned no bytes)", err)
	}
}

func TestRenderer_Screenshot_EmptyFileRejected(t *testing.T) {
	r := New(Config{Exec: stubExec(t, ExecResult{}, nil)})
	_, err := r.Screenshot(context.Background(), "")
	if !errors.Is(err, ErrEmptyNotebookFile) {
		t.Errorf("err = %v, want ErrEmptyNotebookFile", err)
	}
}

func TestRenderer_DefaultsApplied(t *testing.T) {
	called := false
	r := New(Config{
		Exec: func(ctx context.Context, req ExecRequest) (ExecResult, error) {
			called = true
			if req.WaitTimeout != DefaultWaitTimeout {
				t.Errorf("default wait timeout = %v", req.WaitTimeout)
			}
			if req.IdleSelector != IdleSelector {
				t.Errorf("default idle selector = %q", req.IdleSelector)
			}
			return ExecResult{HadChartCell: true, PNG: []byte("ok")}, nil
		},
	})
	if _, err := r.Screenshot(context.Background(), "PUL-1.py"); err != nil {
		t.Fatalf("screenshot: %v", err)
	}
	if !called {
		t.Error("exec not called")
	}
}

func TestRenderer_ContextCancellation(t *testing.T) {
	// Stub exec that just blocks on ctx.
	r := New(Config{
		WaitTimeout: 50 * time.Millisecond,
		Exec: func(ctx context.Context, req ExecRequest) (ExecResult, error) {
			<-ctx.Done()
			return ExecResult{}, ctx.Err()
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := r.Screenshot(ctx, "PUL-303.py")
	if err == nil {
		t.Fatal("expected error on cancelled ctx")
	}
}

// stubExec returns an Exec function that asserts incoming ExecRequest is
// internally consistent and then returns the provided result.
func stubExec(t *testing.T, res ExecResult, err error) Exec {
	t.Helper()
	return func(ctx context.Context, req ExecRequest) (ExecResult, error) {
		if req.URL == "" {
			t.Error("ExecRequest.URL empty")
		}
		if req.ReadySelector == "" {
			t.Error("ExecRequest.ReadySelector empty")
		}
		if req.CellSelector == "" {
			t.Error("ExecRequest.CellSelector empty")
		}
		if req.WaitTimeout <= 0 {
			t.Error("ExecRequest.WaitTimeout non-positive")
		}
		return res, err
	}
}
