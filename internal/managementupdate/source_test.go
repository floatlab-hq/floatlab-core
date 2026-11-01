package managementupdate

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type upstreamTransport func(*http.Request) (*http.Response, error)

func (f upstreamTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestReleaseDiscoveryUsesUpstream(t *testing.T) {
	seen := ""
	transport := upstreamTransport(func(r *http.Request) (*http.Response, error) {
		seen = r.URL.String()
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("[]")), Header: make(http.Header)}, nil
	})
	previous := http.DefaultTransport
	http.DefaultTransport = transport
	t.Cleanup(func() { http.DefaultTransport = previous })
	for _, upstream := range []string{"", "my-org/core"} {
		if _, err := Releases(context.Background(), upstream); err != nil {
			t.Fatal(err)
		}
		repo := upstream
		if repo == "" {
			repo = "floatlab-hq/floatlab-core"
		}
		want := "https://api.github.com/repos/" + repo + "/releases?per_page=100"
		if seen != want {
			t.Fatalf("got %s want %s", seen, want)
		}
	}
	seen = ""
	if _, err := Releases(context.Background(), "../invalid"); err == nil || seen != "" {
		t.Fatal("invalid upstream fetched")
	}
	s := &Status{TargetRelease: "manage-v1.2.3", Upstream: "my-org/core"}
	// The empty response fails manifest decoding before any production access.
	if err := (&Linux{}).Prepare(context.Background(), s); err == nil {
		t.Fatal("invalid manifest accepted")
	}
	if seen != "https://github.com/my-org/core/releases/download/manage-v1.2.3/manage-manifest.json" {
		t.Fatal(seen)
	}
}
