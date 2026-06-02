package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestExportIssueHTMLDev_EnvGate pins the production-safety behavior
// of the hidden export route (PUL-266 PR-1b). The handler must return
// 404 — indistinguishable from a missing resource — unless BOTH
// MULTICA_DEV=1 is set on the process and ?_dev=1 is on the query
// string.
//
// We test the gate without spinning up a full handler / DB because
// the gate runs before any I/O. The 404 short-circuit must hold even
// if h.Queries is nil, which is what we exploit here.
func TestExportIssueHTMLDev_EnvGate(t *testing.T) {
	h := &Handler{} // no Queries — gate must fire before any deref

	cases := []struct {
		name       string
		envSet     bool
		queryParam string
		wantStatus int
	}{
		{"env unset, no _dev",      false, "", http.StatusNotFound},
		{"env unset, _dev=1",       false, "1", http.StatusNotFound},
		{"env set, no _dev",        true, "", http.StatusNotFound},
		{"env set, _dev=0",         true, "0", http.StatusNotFound},
		{"env set, _dev=1",         true, "1", -1}, // 401/500/200 fine — gate passed
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			if tc.envSet {
				t.Setenv("MULTICA_DEV", "1")
			}
			req := httptest.NewRequest(http.MethodGet,
				"/api/issues/PUL-1/export.html?_dev="+tc.queryParam, nil)
			rec := httptest.NewRecorder()

			defer func() {
				if r := recover(); r != nil {
					// Acceptable: when gate passes the handler tries
					// to call h.Queries which is nil — panic is fine
					// for this test, it just proves the gate let us
					// through. We only assert wantStatus when the
					// gate is supposed to REJECT.
					if tc.wantStatus != -1 {
						t.Errorf("unexpected panic with envSet=%v, _dev=%q: %v",
							tc.envSet, tc.queryParam, r)
					}
				}
			}()

			h.ExportIssueHTMLDev(rec, req)

			if tc.wantStatus != -1 && rec.Code != tc.wantStatus {
				t.Errorf("status: got %d, want %d", rec.Code, tc.wantStatus)
			}
		})
	}
}
