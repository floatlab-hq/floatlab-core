package control

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestUpdateMutationFence(t *testing.T) {
	directory := t.TempDir()
	t.Setenv("FLOATLAB_ACTIVATION_DIR", directory)
	handler := updateMutationFence(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	check := func(method, path string, want int) {
		t.Helper()
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(method, path, nil))
		if response.Code != want {
			t.Fatalf("%s %s: got %d, want %d", method, path, response.Code, want)
		}
	}
	check("POST", "/api/v1/stacks", 204)
	if err := os.WriteFile(filepath.Join(directory, "blocked"), []byte("operation"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{"POST", "PUT", "PATCH", "DELETE"} {
		check(method, "/api/v1/stacks/example", 503)
	}
	check("GET", "/api/v1/stacks", 204)
	check("POST", "/api/v1/auth/token", 204)
	check("POST", "/api/v1/nodes/local/updates/host/rollback", 204)
	check("POST", "/api/v1/stats/webhook", 503)
	t.Setenv("FLOATLAB_PREFLIGHT", "true")
	check("POST", "/api/v1/nodes/local/updates/host/rollback", 503)
	check("GET", "/api/v1/stacks", 204)
	check("POST", "/api/v1/auth/token", 204)
}

func TestUpdateDrainWaitsForAdmittedRequestsAndEvaluator(t *testing.T) {
	directory := t.TempDir()
	t.Setenv("FLOATLAB_ACTIVATION_DIR", directory)
	t.Setenv("FLOATLAB_PREFLIGHT", "false")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	evaluatorStarted, evaluatorStopped := make(chan struct{}), make(chan struct{})
	supervisorDone := make(chan struct{})
	go func() {
		defer close(supervisorDone)
		runUpdateBackground(ctx, func(ctx context.Context) { close(evaluatorStarted); <-ctx.Done(); close(evaluatorStopped) })
	}()
	defer func() { cancel(); <-supervisorDone }()
	<-evaluatorStarted
	requestStarted, finishRequest, requestDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
	handler := updateMutationFence(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { close(requestStarted); <-finishRequest }))
	go func() {
		defer close(requestDone)
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/api/v1/stacks", nil))
	}()
	<-requestStarted
	if err := os.WriteFile(filepath.Join(directory, "blocked"), []byte("operation"), 0600); err != nil {
		t.Fatal(err)
	}
	select {
	case <-evaluatorStopped:
	case <-time.After(time.Second):
		t.Fatal("evaluator did not drain")
	}
	if _, err := os.Stat(filepath.Join(directory, "api-drained")); !os.IsNotExist(err) {
		t.Fatal("API drain acknowledged before admitted mutation completed")
	}
	close(finishRequest)
	<-requestDone
	deadline := time.Now().Add(time.Second)
	for {
		if _, err := os.Stat(filepath.Join(directory, "api-drained")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("API did not acknowledge drained state")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
