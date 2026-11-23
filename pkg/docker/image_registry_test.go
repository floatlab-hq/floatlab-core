package docker

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/floatlab/floatlab-core/pkg/ipc"
)

func TestRegistryImagePinsAndUpgradeScopes(t *testing.T) {
	digest := func(character string) string { return "sha256:" + strings.Repeat(character, 64) }
	tags := map[string]string{
		"latest": digest("a"), "2": digest("a"), "2.13": digest("a"), "2.13.3": digest("a"),
		"2.13.10": digest("b"), "2.14.0": digest("c"), "3.0.0": digest("d"),
		"4.0.0-rc1": digest("e"), "2.13.9-alpine": digest("f"), "2.13.4-alpine": digest("1"),
		"stable": digest("c"),
	}
	registry := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v2/":
			w.WriteHeader(http.StatusOK)
		case r.URL.Path == "/v2/fixture/tags/list":
			var names []string
			for tag := range tags {
				names = append(names, tag)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"tags": names})
		case r.Method == http.MethodHead && strings.HasPrefix(r.URL.Path, "/v2/fixture/manifests/"):
			value, ok := tags[strings.TrimPrefix(r.URL.Path, "/v2/fixture/manifests/")]
			if !ok {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
			w.Header().Set("Docker-Content-Digest", value)
			w.Header().Set("Content-Length", "123")
		default:
			http.NotFound(w, r)
		}
	}))
	defer registry.Close()
	repo := strings.TrimPrefix(registry.URL, "http://") + "/fixture:"
	for _, tc := range []struct{ name, tag, policy, current, want string }{
		{"same content as latest", "latest", "", "", "2.13.3@" + digest("a")},
		{"partial version", "2.13", "", "", "2.13.3@" + digest("a")},
		{"already specific", "2.13.3", "", "", "2.13.3@" + digest("a")},
		{"patch", "2.13.3", "patch", "a", "2.13.10@" + digest("b")},
		{"minor", "2.13.3", "minor", "a", "2.14.0@" + digest("c")},
		{"major", "2.13.3", "major", "a", "3.0.0@" + digest("d")},
		{"disabled discovery", "2.13.3", "disabled", "a", "3.0.0@" + digest("d")},
		{"alpine variant", "2.13.4-alpine", "major", "1", "2.13.9-alpine@" + digest("f")},
		{"named save", "stable", "", "", "stable@" + digest("c")},
		{"named update", "stable", "patch", "a", "stable@" + digest("c")},
		{"named unchanged", "stable", "patch", "c", ""},
		{"legacy baseline", "latest", "patch", "a", "2.13.10@" + digest("b")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			policy := ipc.ImageLookupPayload{Image: repo + tc.tag, Policy: tc.policy}
			if tc.current != "" {
				policy.Digests = []string{digest(tc.current)}
			}
			result, err := LookupImage(t.Context(), policy)
			want := tc.want
			if want != "" {
				want = repo + want
			}
			if err != nil || result.Image != want {
				t.Fatalf("image=%q want=%q err=%v", result.Image, want, err)
			}
		})
	}
}
