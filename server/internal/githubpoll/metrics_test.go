package githubpoll

import (
	"sync"
	"testing"
)

func TestMetrics_IncAndSnapshot(t *testing.T) {
	m := NewMetrics()
	m.IncPanic()
	m.IncCall("rabbeet/Pulse", 200)
	m.IncCall("rabbeet/Pulse", 200)
	m.IncCall("rabbeet/Pulse", 304)
	m.IncEvent("rabbeet/Pulse", "ci_failure")
	m.IncEvent("rabbeet/Pulse", "skip")
	m.IncEvent("rabbeet/multica", "pr_merged")
	m.IncSinkError("rabbeet/Pulse")
	m.SetRateLimitRemaining("rabbeet/Pulse", 4321)
	m.SetCursorAge("rabbeet/Pulse", 17)
	// Override should win.
	m.SetRateLimitRemaining("rabbeet/Pulse", 4320)

	s := m.Snapshot()
	if s.Panics != 1 {
		t.Errorf("Panics = %d, want 1", s.Panics)
	}
	if s.Calls["rabbeet/Pulse|200"] != 2 {
		t.Errorf("Calls[rabbeet/Pulse|200] = %d, want 2", s.Calls["rabbeet/Pulse|200"])
	}
	if s.Calls["rabbeet/Pulse|304"] != 1 {
		t.Errorf("Calls[rabbeet/Pulse|304] = %d, want 1", s.Calls["rabbeet/Pulse|304"])
	}
	if s.Events["rabbeet/Pulse|ci_failure"] != 1 || s.Events["rabbeet/Pulse|skip"] != 1 ||
		s.Events["rabbeet/multica|pr_merged"] != 1 {
		t.Errorf("Events bad: %+v", s.Events)
	}
	if s.SinkErrors["rabbeet/Pulse"] != 1 {
		t.Errorf("SinkErrors = %+v", s.SinkErrors)
	}
	if s.RateLimitRemaining["rabbeet/Pulse"] != 4320 {
		t.Errorf("RateLimitRemaining gauge did not overwrite: %d", s.RateLimitRemaining["rabbeet/Pulse"])
	}
	if s.CursorAgeSeconds["rabbeet/Pulse"] != 17 {
		t.Errorf("CursorAgeSeconds = %d, want 17", s.CursorAgeSeconds["rabbeet/Pulse"])
	}
}

func TestMetrics_NilSafe(t *testing.T) {
	// All hot-path methods should accept a nil receiver (tests
	// commonly construct a poller without a metrics instance —
	// nil-tolerance keeps the hot path branch-free instead of
	// requiring a guard at every Inc call site).
	var m *Metrics
	m.IncPanic()
	m.IncCall("x", 200)
	m.IncEvent("x", "ci_failure")
	m.IncSinkError("x")
	m.SetRateLimitRemaining("x", 5)
	m.SetCursorAge("x", 5)
	if m.Snapshot().Panics != 0 {
		t.Error("nil snapshot should produce zero panics")
	}
}

func TestMetrics_Concurrent(t *testing.T) {
	// Hot path is invoked from multiple goroutines (per-repo
	// tickOne under a future fan-out). Smoke test for race detector
	// (`go test -race`): N writers + 1 snapshot reader, no crashes,
	// counts add up.
	m := NewMetrics()
	const N = 100
	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m.IncCall("rabbeet/Pulse", 200)
			m.IncEvent("rabbeet/Pulse", "ci_failure")
		}()
	}
	// Reader concurrent with writers.
	for i := 0; i < 10; i++ {
		go m.Snapshot()
	}
	wg.Wait()
	s := m.Snapshot()
	if s.Calls["rabbeet/Pulse|200"] != N {
		t.Errorf("Calls = %d, want %d", s.Calls["rabbeet/Pulse|200"], N)
	}
	if s.Events["rabbeet/Pulse|ci_failure"] != N {
		t.Errorf("Events = %d, want %d", s.Events["rabbeet/Pulse|ci_failure"], N)
	}
}

func TestItoa(t *testing.T) {
	cases := []struct {
		in   int
		want string
	}{
		{0, "0"},
		{-1, "0"},
		{1, "1"},
		{200, "200"},
		{304, "304"},
		{9999, "9999"},
	}
	for _, tc := range cases {
		if got := itoa(tc.in); got != tc.want {
			t.Errorf("itoa(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
