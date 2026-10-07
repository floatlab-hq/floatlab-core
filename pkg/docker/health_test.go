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
