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

func TestQueryLocalDoesNotAskLeaderForCatchupProof(t *testing.T) {
	client := NewClient("http://joining-node")
	client.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "joining-node" || r.URL.Query().Get("level") != "none" {
			t.Fatalf("catch-up read can use leader state: %s", r.URL)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"results":[{"columns":["node_id"],"values":[["joining"]]}]}`))}, nil
	})
	result, err := client.QueryLocal(context.Background(), Statement{SQL: "SELECT node_id FROM lab_members"})
	if err != nil || len(result.Values) != 1 {
		t.Fatalf("local catch-up query: %+v %v", result, err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestEmptyExecuteDoesNotSendInvalidRQLiteRequest(t *testing.T) {
	client := NewClient("http://rqlite")
	client.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		t.Fatal("empty migration batch reached rqlite")
		return nil, nil
	})
	if err := client.Execute(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
}

func TestLeaderRedirectPreservesWriteAndEncryption(t *testing.T) {
	for _, scheme := range []string{"https", "http"} {
		t.Run(scheme, func(t *testing.T) {
			client := NewClient("https://follower")
			calls := 0
			client.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				if calls == 1 {
					return &http.Response{StatusCode: 301, Header: http.Header{"Location": []string{scheme + "://leader/db/execute?transaction"}}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
				}
				body, _ := io.ReadAll(r.Body)
				if r.Method != "POST" || !strings.Contains(string(body), "INSERT") {
					t.Fatalf("redirect lost write: %s %s", r.Method, body)
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"results":[{}]}`)), Request: r}, nil
			})
			err := client.Execute(context.Background(), []Statement{{SQL: "INSERT INTO test VALUES(1)"}})
			if scheme == "http" {
				if err == nil || calls != 1 {
					t.Fatalf("accepted TLS downgrade: %v calls=%d", err, calls)
				}
			} else if err != nil || calls != 2 {
				t.Fatalf("redirect failed: %v calls=%d", err, calls)
			}
		})
	}
}
