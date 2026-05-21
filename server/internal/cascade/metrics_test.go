package cascade

import "testing"

// TestMetrics_IncLegacyEventDropped_NilSafe locks in the nil-receiver
// contract every Worker call site currently relies on: pre-PUL-220
// constructors that don't pass a *Metrics (and tests that
// intentionally exercise the unwired path) must not panic when the
// CQ2 block fires.
func TestMetrics_IncLegacyEventDropped_NilSafe(t *testing.T) {
	var m *Metrics
	m.IncLegacyEventDropped("ci_failure")
	snap := m.Snapshot()
	if snap.LegacyEventDropped != nil {
		t.Errorf("nil receiver snapshot must yield nil map, got %v", snap.LegacyEventDropped)
	}
}

// TestMetrics_IncLegacyEventDropped_CountsAcceptedTypes is the happy
// path: both whitelisted values increment independently and the
// snapshot reads back the exact counts.
func TestMetrics_IncLegacyEventDropped_CountsAcceptedTypes(t *testing.T) {
	m := NewMetrics()
	m.IncLegacyEventDropped("ci_failure")
	m.IncLegacyEventDropped("ci_failure")
	m.IncLegacyEventDropped("pr_review_change")

	snap := m.Snapshot()
	if got := snap.LegacyEventDropped["ci_failure"]; got != 2 {
		t.Errorf("ci_failure count = %d, want 2", got)
	}
	if got := snap.LegacyEventDropped["pr_review_change"]; got != 1 {
		t.Errorf("pr_review_change count = %d, want 1", got)
	}
}

// TestMetrics_IncLegacyEventDropped_RejectsUnknown guards the
// cardinality whitelist documented on IncLegacyEventDropped. A
// future caller passing a non-CQ2 event_type must NOT register a
// new time-series label.
func TestMetrics_IncLegacyEventDropped_RejectsUnknown(t *testing.T) {
	m := NewMetrics()
	m.IncLegacyEventDropped("pr_merged")
	m.IncLegacyEventDropped("pr_title_edit")
	m.IncLegacyEventDropped("")

	snap := m.Snapshot()
	if len(snap.LegacyEventDropped) != 0 {
		t.Errorf("whitelist breach: snapshot has %d entries, want 0: %v",
			len(snap.LegacyEventDropped), snap.LegacyEventDropped)
	}
}

// TestMetrics_Snapshot_FreshIsEmpty: a NewMetrics() with no Inc
// calls produces a zero-value snapshot. The collector relies on
// "no key in the map" → "emit no sample" so an unused counter
// doesn't pollute /metrics with a meaningless 0-value series.
func TestMetrics_Snapshot_FreshIsEmpty(t *testing.T) {
	m := NewMetrics()
	snap := m.Snapshot()
	if len(snap.LegacyEventDropped) != 0 {
		t.Errorf("fresh Metrics snapshot must have empty map, got %v", snap.LegacyEventDropped)
	}
}
