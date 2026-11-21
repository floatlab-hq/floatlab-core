package store

import "testing"

func TestPoolScrubStatus(t *testing.T) {
	for _, test := range []struct {
		name, scan, wantState string
	}{
		{"no scan", `{}`, ""},
		{"resilver", `{"function":"RESILVER","state":"SCANNING"}`, ""},
		{"running", `{"function":"SCRUB","state":"SCANNING","start_time":1700000000,"issued":25,"to_examine":100}`, "running"},
		{"paused", `{"function":"SCRUB","state":"SCANNING","scrub_pause":1700000001}`, "paused"},
		{"completed with errors", `{"function":"SCRUB","state":"FINISHED","end_time":1700000002,"processed":4096,"errors":2}`, "completed"},
		{"canceled", `{"function":"SCRUB","state":"CANCELED"}`, "canceled"},
	} {
		t.Run(test.name, func(t *testing.T) {
			status, err := parsePoolStatusJSON(`{"pools":{"floatlab":{"state":"ONLINE","error_count":0,"scan_stats":`+test.scan+`}}}`, "floatlab")
			if err != nil {
				t.Fatal(err)
			}
			if status.Health != "ONLINE" || status.Errors != "No known data errors" {
				t.Fatalf("unexpected pool health: %+v", status)
			}
			scrub := status.Scrub
			if test.wantState == "" {
				if scrub != nil {
					t.Fatalf("expected no scrub, got %+v", scrub)
				}
				return
			}
			if scrub == nil || scrub.State != test.wantState {
				t.Fatalf("expected scrub state %q, got %+v", test.wantState, scrub)
			}
			switch scrub.State {
			case "running":
				if scrub.StartedAt == nil || scrub.StartedAt.Unix() != 1700000000 ||
					scrub.CompletedAt != nil || scrub.ProgressPct == nil || *scrub.ProgressPct != 25 {
					t.Fatalf("unexpected running scrub: %+v", scrub)
				}
			case "completed":
				if scrub.CompletedAt == nil || scrub.CompletedAt.Unix() != 1700000002 ||
					scrub.ProgressPct != nil || scrub.RepairedBytes != 4096 || scrub.Errors != 2 {
					t.Fatalf("unexpected completed scrub: %+v", scrub)
				}
			}
		})
	}
}
