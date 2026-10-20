package stats

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestInstantQueryBoundsLookbackAndKeepsZero(t *testing.T) {
	at := time.Unix(1000, 0).UTC()
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/api/v1/query" || r.URL.Query().Get("max_lookback") != "2m0s" || r.URL.Query().Get("nocache") != "1" || r.URL.Query().Get("time") != "1000.000" {
			t.Errorf("unexpected request %s", r.URL)
		}
		value := "0"
		if strings.HasPrefix(r.URL.Query().Get("query"), "timestamp(") {
			value = "995"
		}
		fmt.Fprintf(w, `{"status":"success","data":{"resultType":"vector","result":[{"metric":{"node_id":"node"},"value":[1000,%q]}]}}`, value)
	}))
	defer server.Close()
	rows, err := NewClient(server.URL).QueryInstant(context.Background(), "up == 0", at, 2*time.Minute)
	if err != nil || len(rows) != 1 || rows[0].Value != 0 || !rows[0].ObservedAt.Equal(at) || requests != 1 {
		t.Fatalf("%+v %v (%d requests)", rows, err, requests)
	}
}

func TestInstantQueryRejectsUnusableResponses(t *testing.T) {
	for name, body := range map[string]string{
		"error":         `{"status":"error","error":"invalid query"}`,
		"partial":       `{"status":"success","isPartial":true,"data":{"resultType":"vector","result":[]}}`,
		"scalar":        `{"status":"success","data":{"resultType":"scalar","result":[1,"1"]}}`,
		"bad_value":     `{"status":"success","data":{"resultType":"vector","result":[{"value":[1,"oops"]}]}}`,
		"nan":           `{"status":"success","data":{"resultType":"vector","result":[{"value":[1,"NaN"]}]}}`,
		"missing_value": `{"status":"success","data":{"resultType":"vector","result":[{"value":[]}]}}`,
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) }))
			defer server.Close()
			if _, err := NewClient(server.URL).QueryInstant(context.Background(), "up", time.Now(), time.Minute); err == nil {
				t.Fatal("expected unusable response to fail")
			}
		})
	}
}
