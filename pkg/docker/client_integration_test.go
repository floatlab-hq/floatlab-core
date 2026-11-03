package docker

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

// Exercise the actual Compose SDK and daemon: a cached :latest image must not
// contact its registry during start, while an explicit upgrade must do so.
func runTestComposeCachedStartAndExplicitPull(t ginkgo.GinkgoTInterface) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	var requests atomic.Int64
	registry := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.Error(w, "registry unavailable", http.StatusServiceUnavailable)
	}))
	defer registry.Close()
	const source = "rqlite/rqlite:8.26.0@sha256:b81e945809e1fcc92f3d72c07d7f8726252dfb5ed406db6faaf6879159193fe9"
	docker := func(args ...string) {
		t.Helper()
		out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("docker %v: %s %v", args, out, err)
		}
	}
	if err := exec.CommandContext(ctx, "docker", "image", "inspect", source).Run(); err != nil {
		docker("pull", source)
	}
	project := fmt.Sprintf("floatlab-offline-%d", time.Now().UnixNano())
	image := strings.TrimPrefix(registry.URL, "http://") + "/" + project + ":latest"
	docker("tag", source, image)
	t.Cleanup(func() { exec.Command("docker", "image", "rm", image).Run() })
	path := filepath.Join(t.TempDir(), "compose.yaml")
	if err := os.WriteFile(path, []byte("services:\n  web:\n    image: "+image+"\n    entrypoint: [/bin/rqlited]\n    command: [-version]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	client, err := New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		defer client.Close()
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := client.ComposeDown(cleanupCtx, project, path, true); err != nil {
			t.Error("clean up owned compose project", err)
		}
	})
	if err := client.ComposeUp(ctx, project, path); err != nil {
		t.Fatal("cached image start contacted unavailable registry", err)
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("cached default startup made %d registry requests", got)
	}
	if err := client.ComposePull(ctx, project, path, nil); err == nil {
		t.Fatal("explicit upgrade pull unexpectedly accepted unavailable registry")
	}
	if requests.Load() == 0 {
		t.Fatal("explicit upgrade did not contact image registry")
	}
	requests.Store(0)
	if err := os.WriteFile(path, []byte("services:\n  web:\n    image: "+image+"\n    pull_policy: never\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := client.ComposePull(ctx, project, path, nil); err != nil || requests.Load() != 0 {
		t.Fatalf("explicit never policy ignored: requests=%d err=%v", requests.Load(), err)
	}
	if err := os.WriteFile(path, []byte("services:\n  web:\n    image: "+image+"\n    pull_policy: always\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := client.ComposeUp(ctx, project, path); err == nil || requests.Load() == 0 {
		t.Fatalf("explicit always policy ignored: requests=%d err=%v", requests.Load(), err)
	}
}

func TestDockerIntegration(t *testing.T) {
	if os.Getenv("FLOATLAB_DOCKER_INTEGRATION") != "1" {
		t.Skip("run the disposable-appliance integration suite")
	}
	gomega.RegisterFailHandler(ginkgo.Fail)
	config, reporter := ginkgo.GinkgoConfiguration()
	config.FailOnEmpty = true
	ginkgo.RunSpecs(t, "TestDockerIntegration", config, reporter)
}

var _ = ginkgo.Describe("TestDockerIntegration", ginkgo.Serial, func() {
	ginkgo.It("TestComposeCachedStartAndExplicitPull", func() { runTestComposeCachedStartAndExplicitPull(ginkgo.GinkgoT()) })
})
