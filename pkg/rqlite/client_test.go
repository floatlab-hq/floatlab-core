package rqlite

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestRequestUsesWriteCapableEndpoint(t *testing.T) {
	client := NewClient("http://rqlite")
	client.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/db/request" || !r.URL.Query().Has("transaction") {
			t.Fatalf("unexpected endpoint: %s", r.URL.String())
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"results":[{"rows_affected":1}]}`))}, nil
	})

	result, err := client.Request(context.Background(), Statement{SQL: "UPDATE tasks SET state='running' RETURNING id"})
	if err != nil || result.RowsAffected != 1 {
		t.Fatalf("Request() = %#v, %v", result, err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
