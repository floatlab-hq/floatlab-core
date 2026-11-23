package docker

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	dockerclient "github.com/moby/moby/client"
)

func TestContainerLifecycleCallsDockerAPI(t *testing.T) {
	requests := make([]string, 0, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.Path)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	dc, err := dockerclient.NewClientWithOpts(dockerclient.WithHost(server.URL), dockerclient.WithVersion("1.55"))
	if err != nil {
		t.Fatal(err)
	}
	client := &Client{dc: dc}
	if err := client.StartContainer(context.Background(), "demo"); err != nil {
		t.Fatal(err)
	}
	if err := client.StopContainer(context.Background(), "demo"); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(requests, "\n"); !strings.Contains(got, "POST /v1.55/containers/demo/start") || !strings.Contains(got, "POST /v1.55/containers/demo/stop") {
		t.Fatalf("unexpected Docker requests: %s", got)
	}
}

type healthTestTransport func(*http.Request) (*http.Response, error)

func (adapter healthTestTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return adapter(req)
}

func TestContainerDetailsKeepProcessAndProbeStateSeparate(t *testing.T) {
	httpClient := &http.Client{Transport: healthTestTransport(func(r *http.Request) (*http.Response, error) {
		w := httptest.NewRecorder()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(r.URL.Path, "/restarting/"):
			_, _ = w.Write([]byte(`{"State":{"Status":"running","Restarting":true,"Health":{"Status":"healthy"}}}`))
		case strings.Contains(r.URL.Path, "/healthy/"):
			_, _ = w.Write([]byte(`{"State":{"Status":"running","Health":{"Status":"healthy"}}}`))
		case strings.Contains(r.URL.Path, "/no-check/"):
			_, _ = w.Write([]byte(`{"State":{"Status":"running"}}`))
		default:
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"message":"inspect failed"}`))
		}
		return w.Result(), nil
	})}
	dc, err := dockerclient.NewClientWithOpts(dockerclient.WithHost("http://docker.test"), dockerclient.WithVersion("1.55"), dockerclient.WithHTTPClient(httpClient))
	if err != nil {
		t.Fatal(err)
	}
	defer dc.Close()
	client := &Client{dc: dc}
	for _, test := range []struct{ id, state, health string }{
		{"restarting", "restarting", "none"},
		{"healthy", "running", "healthy"},
		{"no-check", "running", "none"},
		{"unknown", "running", "unknown"},
	} {
		state, health, _ := containerDetails(context.Background(), client, test.id, "running")
		if state != test.state || health != test.health {
			t.Errorf("%s state %q, health %q; want %q, %q", test.id, state, health, test.state, test.health)
		}
	}
}
