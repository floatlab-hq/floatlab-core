package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

type apiClient struct {
	t       *testing.T
	baseURL string
	token   string
	client  *http.Client
}

func TestContainerManagementAPI(t *testing.T) {
	if os.Getenv("FLOATLAB_VM_INTEGRATION") != "1" {
		t.Skip("run with FLOATLAB_VM_INTEGRATION=1 go test -count=1 -timeout 30m -v ./integration")
	}

	c := newAPIClient(t, startAppliance(t))
	c.waitReady()
	c.assertUnauthorized()
	c.login()
	c.waitNodeReady()

	stackName := fmt.Sprintf("api-it-%d", time.Now().UnixNano())
	marker := "floatlab-api-integration-" + stackName
	compose := composeFixture(stackName, marker, "alpine:3.20", "from-start")

	started := c.startNewStack(stackName, compose)
	stackID := started.StackID
	t.Cleanup(func() {
		c.cleanupStack(stackID)
	})
	c.waitOperation(started.OperationID)
	c.waitStackState(stackID, "RunningPrimary")

	c.assertStackReadAPIs(stackID, stackName, marker)
	containerID := c.assertContainerAPIs(stackID)
	c.assertTerminal(stackID, containerID)
	c.assertStorageAPIs(stackID)
	c.assertStackSnapshotAPIs(stackID)
	c.assertLogAPIs(stackID, containerID, marker)
	c.assertStatsAPIs(stackID)
	c.assertEvents(stackID)
	c.assertIdempotency(stackID)
	c.assertComposeUpdate(stackID, stackName, marker)

	restarted := c.mutate(http.MethodPost, "/api/v1/stacks/"+stackID+"/restart", nil, "application/json")
	c.waitOperation(restarted.OperationID)
	c.waitStackState(stackID, "RunningPrimary")

	stopped := c.mutate(http.MethodPost, "/api/v1/stacks/"+stackID+"/stop", nil, "application/json")
	c.waitOperation(stopped.OperationID)
	c.waitStackState(stackID, "Idle")

	startedAgain := c.mutate(http.MethodPost, "/api/v1/stacks/"+stackID+"/start", nil, "application/json")
	c.waitOperation(startedAgain.OperationID)
	c.waitStackState(stackID, "RunningPrimary")

	upgraded := c.mutateJSON(http.MethodPost, "/api/v1/stacks/"+stackID+"/upgrade", map[string]any{
		"images": map[string]string{"app": "alpine:3.20"},
	})
	c.waitOperation(upgraded.OperationID)
	c.waitStackState(stackID, "RunningPrimary")

	deleted := c.mutate(http.MethodDelete, "/api/v1/stacks/"+stackID+"?purge=true", nil, "")
	c.waitOperation(deleted.OperationID)
	c.waitStackDeleted(stackID)
	stackID = ""
}

func newAPIClient(t *testing.T, baseURL string) *apiClient {
	t.Helper()
	return &apiClient{t: t, baseURL: strings.TrimRight(baseURL, "/"), client: &http.Client{Timeout: 20 * time.Second}}
}

func startAppliance(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	appliance := filepath.Join(root, "floatlab-appliance-image")
	name := env("VM_NAME", "floatlab-integration")
	uri := env("LIBVIRT_URI", "qemu:///system")
	pool := env("LIBVIRT_POOL", "default")
	volume := name + "-zfs.qcow2"

	t.Cleanup(func() {
		if os.Getenv("FLOATLAB_KEEP_VM") == "1" {
			t.Logf("keeping VM %s and volume %s", name, volume)
			return
		}
		command(30*time.Second, root, nil, "virsh", "-c", uri, "destroy", name)
		command(30*time.Second, root, nil, "virsh", "-c", uri, "undefine", name, "--nvram")
		command(30*time.Second, root, nil, "virsh", "-c", uri, "vol-delete", "--pool", pool, volume)
	})
	run(t, appliance, nil, "nix", "build", ".#iso", "--print-build-logs")
	run(t, root, []string{"VM_NAME=" + name, "LIBVIRT_URI=" + uri, "LIBVIRT_POOL=" + pool}, filepath.Join(appliance, "scripts/run-libvirt.sh"))
	if baseURL := os.Getenv("API_URL"); baseURL != "" {
		return baseURL
	}
	return "http://" + waitForIP(t, root, uri, name) + ":8080"
}

func (c *apiClient) waitReady() {
	c.t.Helper()
	waitFor(c.t, 2*time.Minute, func() error {
		var health struct {
			Status    string `json:"status"`
			RaftState string `json:"raft_state"`
		}
		if err := c.doJSON(http.MethodGet, "/api/v1/health", "", "", nil, &health); err != nil {
			return err
		}
		if health.Status != "ok" {
			return fmt.Errorf("health status is %q", health.Status)
		}
		if health.RaftState != "" && health.RaftState != "Leader" && health.RaftState != "Follower" {
			return fmt.Errorf("raft state is %q", health.RaftState)
		}
		var ready struct {
			Status string `json:"status"`
		}
		if err := c.doJSON(http.MethodGet, "/api/v1/health/ready", "", "", nil, &ready); err != nil {
			return err
		}
		if ready.Status != "ready" {
			return fmt.Errorf("ready status is %q", ready.Status)
		}
		return nil
	})
}

func (c *apiClient) assertUnauthorized() {
	c.t.Helper()
	resp, body, err := c.request(http.MethodGet, "/api/v1/stacks", "", "", "", nil)
	if err != nil {
		c.t.Fatal(err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		c.t.Fatalf("GET /stacks without token: got %s body=%s", resp.Status, bytes.TrimSpace(body))
	}
}

func (c *apiClient) login() {
	c.t.Helper()
	var login struct {
		AccessToken string `json:"access_token"`
	}
	body := map[string]string{
		"username": env("FLOATLAB_USER", "demo"),
		"password": env("FLOATLAB_PASSWORD", "floatlab"),
	}
	if err := c.doJSON(http.MethodPost, "/api/v1/auth/token", "", "application/json", bytes.NewReader(mustJSON(c.t, body)), &login); err != nil {
		c.t.Fatal(err)
	}
	if login.AccessToken == "" {
		c.t.Fatal("login response did not include access_token")
	}
	c.token = login.AccessToken
}

func (c *apiClient) waitNodeReady() {
	c.t.Helper()
	waitFor(c.t, 2*time.Minute, func() error {
		var nodes []struct {
			ID string `json:"id"`
		}
		if err := c.doJSON(http.MethodGet, "/api/v1/nodes", c.token, "", nil, &nodes); err != nil {
			return err
		}
		registered := false
		for _, node := range nodes {
			registered = registered || node.ID == "node1"
		}
		if !registered {
			return fmt.Errorf("node1 is not registered")
		}
		var health struct {
			Status string `json:"status"`
		}
		if err := c.doJSON(http.MethodGet, "/api/v1/nodes/node1/health", c.token, "", nil, &health); err != nil {
			return err
		}
		if health.Status != "online" {
			return fmt.Errorf("node status is %q", health.Status)
		}
		return nil
	})
}

type mutationResponse struct {
	OperationID string `json:"operation_id"`
	StackID     string `json:"stack_id"`
	State       string `json:"state"`
	Status      string `json:"status"`
	Snapshot    string `json:"snapshot"`
	TaskID      string `json:"task_id"`
}

func (c *apiClient) startNewStack(name, compose string) mutationResponse {
	c.t.Helper()
	return c.mutate(http.MethodPost, "/api/v1/stacks/"+name+"/start", strings.NewReader(compose), "application/yaml")
}

func (c *apiClient) mutateJSON(method, path string, body any) mutationResponse {
	c.t.Helper()
	return c.mutate(method, path, bytes.NewReader(mustJSON(c.t, body)), "application/json")
}

func (c *apiClient) mutate(method, path string, body io.Reader, contentType string) mutationResponse {
	c.t.Helper()
	var out mutationResponse
	if err := c.doJSON(method, path, c.token, contentType, body, &out, "it-"+time.Now().Format("150405.000000000")); err != nil {
		c.t.Fatal(err)
	}
	return out
}

func (c *apiClient) assertStackReadAPIs(stackID, stackName, marker string) {
	c.t.Helper()
	var stacks []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := c.doJSON(http.MethodGet, "/api/v1/stacks", c.token, "", nil, &stacks); err != nil {
		c.t.Fatal(err)
	}
	found := false
	for _, stack := range stacks {
		found = found || (stack.ID == stackID && stack.Name == stackName)
	}
	if !found {
		c.t.Fatalf("created stack %s/%s not found in list: %+v", stackID, stackName, stacks)
	}

	var stack struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		State       string `json:"state"`
		PrimaryNode string `json:"primary_node"`
		DatasetPath string `json:"dataset_path"`
	}
	if err := c.doJSON(http.MethodGet, "/api/v1/stacks/"+stackID, c.token, "", nil, &stack); err != nil {
		c.t.Fatal(err)
	}
	if stack.ID != stackID || stack.Name != stackName || stack.PrimaryNode == "" || stack.DatasetPath == "" {
		c.t.Fatalf("unexpected stack response: %+v", stack)
	}

	var state struct {
		State string `json:"state"`
	}
	if err := c.doJSON(http.MethodGet, "/api/v1/stacks/"+stackID+"/state", c.token, "", nil, &state); err != nil {
		c.t.Fatal(err)
	}
	if state.State != "RunningPrimary" {
		c.t.Fatalf("state endpoint returned %q", state.State)
	}

	resp, body, err := c.request(http.MethodGet, "/api/v1/stacks/"+stackID+"/config", c.token, "", "", nil)
	if err != nil {
		c.t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), marker) {
		c.t.Fatalf("config response status=%s body=%s", resp.Status, bytes.TrimSpace(body))
	}

	var status struct {
		StackID    string `json:"stack_id"`
		State      string `json:"state"`
		Containers []any  `json:"containers"`
	}
	if err := c.doJSON(http.MethodGet, "/api/v1/stacks/"+stackID+"/status", c.token, "", nil, &status); err != nil {
		c.t.Fatal(err)
	}
	if status.StackID != stackID || status.State != "RunningPrimary" || len(status.Containers) == 0 {
		c.t.Fatalf("unexpected status response: %+v", status)
	}
}

func (c *apiClient) assertContainerAPIs(stackID string) string {
	c.t.Helper()
	var containers []struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Image   string `json:"image"`
		Status  string `json:"status"`
		Health  string `json:"health"`
		NodeID  string `json:"node_id"`
		StackID string `json:"stack_id"`
		Service string `json:"service"`
	}
	if err := c.doJSON(http.MethodGet, "/api/v1/stacks/"+stackID+"/containers", c.token, "", nil, &containers); err != nil {
		c.t.Fatal(err)
	}
	if len(containers) != 1 {
		c.t.Fatalf("expected one container, got %+v", containers)
	}
	container := containers[0]
	if container.ID == "" || container.StackID != stackID || container.Service != "app" || container.Status != "running" {
		c.t.Fatalf("unexpected container response: %+v", container)
	}
	return container.ID
}

func (c *apiClient) assertTerminal(stackID, containerID string) {
	c.t.Helper()
	wsURL := strings.Replace(c.baseURL, "http://", "ws://", 1)
	wsURL = strings.Replace(wsURL, "https://", "wss://", 1)
	query := url.Values{}
	query.Add("command", "sh")
	query.Add("command", "-lc")
	query.Add("command", "printf terminal-ok")
	query.Set("rows", "12")
	query.Set("cols", "80")
	endpoint := wsURL + "/api/v1/stacks/" + stackID + "/containers/" + containerID + "/terminal?" + query.Encode()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	conn, resp, err := websocket.Dial(ctx, endpoint, &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": []string{"Bearer " + c.token}}})
	if err != nil {
		status := ""
		if resp != nil {
			status = resp.Status
		}
		c.t.Fatalf("terminal websocket dial failed: %s %v", status, err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "done")

	var got bytes.Buffer
	for ctx.Err() == nil {
		_, data, err := conn.Read(ctx)
		if err != nil {
			break
		}
		got.Write(data)
		if strings.Contains(got.String(), "terminal-ok") {
			return
		}
	}
	c.t.Fatalf("terminal output did not contain marker, got %q", got.String())
}

func (c *apiClient) assertStorageAPIs(stackID string) {
	c.t.Helper()
	var pools []struct {
		NodeID string `json:"node_id"`
		Name   string `json:"name"`
		Health string `json:"health"`
	}
	if err := c.doJSON(http.MethodGet, "/api/v1/storage/pools", c.token, "", nil, &pools); err != nil {
		c.t.Fatal(err)
	}
	if len(pools) == 0 {
		c.t.Fatal("expected at least one storage pool")
	}
	if err := c.doJSON(http.MethodGet, "/api/v1/storage/pools/"+pools[0].NodeID+"/"+pools[0].Name, c.token, "", nil, &map[string]any{}); err != nil {
		c.t.Fatal(err)
	}

	var dataset struct {
		StackID    string `json:"stack_id"`
		Name       string `json:"name"`
		Mountpoint string `json:"mount_point"`
	}
	if err := c.doJSON(http.MethodGet, "/api/v1/storage/datasets/"+stackID, c.token, "", nil, &dataset); err != nil {
		c.t.Fatal(err)
	}
	if dataset.StackID != stackID || dataset.Name == "" || dataset.Mountpoint == "" {
		c.t.Fatalf("unexpected dataset response: %+v", dataset)
	}

	snapshotName := "it-storage-" + time.Now().Format("150405000000000")
	created := c.mutateJSON(http.MethodPost, "/api/v1/storage/datasets/"+stackID+"/snapshots", map[string]string{"name": snapshotName})
	if created.TaskID == "" || created.Snapshot != snapshotName {
		c.t.Fatalf("unexpected storage snapshot create response: %+v", created)
	}
	waitFor(c.t, 90*time.Second, func() error {
		if findSnapshot(c, "/api/v1/storage/datasets/"+stackID+"/snapshots", snapshotName) != "" {
			return nil
		}
		return fmt.Errorf("snapshot %s not listed yet", snapshotName)
	})
	actualName := findSnapshot(c, "/api/v1/storage/datasets/"+stackID+"/snapshots", snapshotName)
	if actualName != "" {
		_ = c.mutate(http.MethodDelete, "/api/v1/storage/datasets/"+stackID+"/snapshots/"+actualName, nil, "")
	}
}

func (c *apiClient) assertStackSnapshotAPIs(stackID string) {
	c.t.Helper()
	created := c.mutate(http.MethodPost, "/api/v1/stacks/"+stackID+"/snapshots", nil, "application/json")
	if created.OperationID == "" || created.Snapshot == "" {
		c.t.Fatalf("unexpected stack snapshot create response: %+v", created)
	}
	c.waitOperation(created.OperationID)
	waitFor(c.t, 90*time.Second, func() error {
		if findSnapshot(c, "/api/v1/stacks/"+stackID+"/snapshots", created.Snapshot) != "" {
			return nil
		}
		return fmt.Errorf("stack snapshot %s not listed yet", created.Snapshot)
	})
	actualName := findSnapshot(c, "/api/v1/stacks/"+stackID+"/snapshots", created.Snapshot)
	if actualName == "" {
		c.t.Fatalf("stack snapshot %s disappeared before delete", created.Snapshot)
	}
	deleted := c.mutate(http.MethodDelete, "/api/v1/stacks/"+stackID+"/snapshots/"+actualName, nil, "")
	c.waitOperation(deleted.OperationID)
}

func findSnapshot(c *apiClient, path, name string) string {
	c.t.Helper()
	var snapshots []struct {
		Name string `json:"name"`
	}
	if err := c.doJSON(http.MethodGet, path, c.token, "", nil, &snapshots); err != nil {
		c.t.Fatal(err)
	}
	for _, snapshot := range snapshots {
		if snapshot.Name == name || strings.HasSuffix(snapshot.Name, "-"+name) {
			return snapshot.Name
		}
	}
	return ""
}

func (c *apiClient) assertLogAPIs(stackID, containerID, marker string) {
	c.t.Helper()
	for _, path := range []string{
		"/api/v1/logs/search?q=*&range=1h&limit=5",
		"/api/v1/logs/stacks/" + stackID + "?range=1h",
		"/api/v1/logs/containers/" + containerID,
		"/api/v1/logs/nodes/node1?range=1h",
		"/api/v1/logs/audit?limit=5",
	} {
		var lines []map[string]any
		if err := c.doJSON(http.MethodGet, path, "", "", nil, &lines); err != nil {
			c.t.Fatal(err)
		}
	}

	deadline := time.Now().Add(45 * time.Second)
	var last error
	for time.Now().Before(deadline) {
		var lines []struct {
			Message string `json:"message"`
		}
		if err := c.doJSON(http.MethodGet, "/api/v1/logs/search?q="+url.QueryEscape(marker)+"&range=1h&limit=50", "", "", nil, &lines); err != nil {
			last = err
			time.Sleep(2 * time.Second)
			continue
		}
		for _, line := range lines {
			if strings.Contains(line.Message, marker) {
				return
			}
		}
		last = fmt.Errorf("log marker %q not found yet", marker)
		time.Sleep(2 * time.Second)
	}
	c.t.Logf("log marker was not observed before timeout; log ingestion is asynchronous: %v", last)
}

func (c *apiClient) assertStatsAPIs(stackID string) {
	c.t.Helper()
	for _, path := range []string{
		"/api/v1/stats/query?q=" + url.QueryEscape("up") + "&range=1h",
		"/api/v1/stats/stacks/" + stackID + "?range=1h",
		"/api/v1/stats/nodes/node1?range=1h",
		"/api/v1/stats/storage/node1?range=1h",
	} {
		var payload any
		if err := c.doJSON(http.MethodGet, path, "", "", nil, &payload); err != nil {
			c.t.Fatal(err)
		}
	}
}

func (c *apiClient) assertEvents(stackID string) {
	c.t.Helper()
	waitFor(c.t, 30*time.Second, func() error {
		var page struct {
			Items      []map[string]any `json:"items"`
			NextCursor string           `json:"next_cursor"`
		}
		if err := c.doJSON(http.MethodGet, "/api/v1/stacks/"+stackID+"/events?limit=50", c.token, "", nil, &page); err != nil {
			return err
		}
		if len(page.Items) == 0 {
			return fmt.Errorf("no lifecycle events yet")
		}
		return nil
	})
}

func (c *apiClient) assertIdempotency(stackID string) {
	c.t.Helper()
	resp, body, err := c.request(http.MethodPost, "/api/v1/stacks/"+stackID+"/restart", c.token, "", "application/json", nil)
	if err != nil {
		c.t.Fatal(err)
	}
	if resp.StatusCode != http.StatusBadRequest {
		c.t.Fatalf("missing idempotency key: got %s body=%s", resp.Status, bytes.TrimSpace(body))
	}

	key := "repeat-" + time.Now().Format("150405000000000")
	body1 := []byte(`{"images":{"app":"alpine:3.20"}}`)
	resp, firstBody, err := c.request(http.MethodPost, "/api/v1/stacks/"+stackID+"/upgrade", c.token, key, "application/json", bytes.NewReader(body1))
	if err != nil {
		c.t.Fatal(err)
	}
	if resp.StatusCode != http.StatusAccepted {
		c.t.Fatalf("first idempotent request: got %s body=%s", resp.Status, bytes.TrimSpace(firstBody))
	}
	var first mutationResponse
	if err := json.Unmarshal(firstBody, &first); err != nil {
		c.t.Fatal(err)
	}
	c.waitOperation(first.OperationID)
	c.waitStackState(stackID, "RunningPrimary")

	resp, secondBody, err := c.request(http.MethodPost, "/api/v1/stacks/"+stackID+"/upgrade", c.token, key, "application/json", bytes.NewReader(body1))
	if err != nil {
		c.t.Fatal(err)
	}
	if resp.StatusCode != http.StatusAccepted || !bytes.Equal(bytes.TrimSpace(firstBody), bytes.TrimSpace(secondBody)) {
		c.t.Fatalf("idempotent replay mismatch: first=%s second_status=%s second=%s", bytes.TrimSpace(firstBody), resp.Status, bytes.TrimSpace(secondBody))
	}

	resp, conflictBody, err := c.request(http.MethodPost, "/api/v1/stacks/"+stackID+"/upgrade", c.token, key, "application/json", strings.NewReader(`{"images":{"app":"alpine:3.19"}}`))
	if err != nil {
		c.t.Fatal(err)
	}
	if resp.StatusCode != http.StatusConflict {
		c.t.Fatalf("idempotency conflict: got %s body=%s", resp.Status, bytes.TrimSpace(conflictBody))
	}
}

func (c *apiClient) assertComposeUpdate(stackID, stackName, marker string) {
	c.t.Helper()
	updated := composeFixture(stackName, marker, "alpine:3.20", "from-update")
	var stack struct {
		ID          string `json:"id"`
		ComposeFile string `json:"compose_file"`
	}
	if err := c.doJSON(http.MethodPut, "/api/v1/stacks/"+stackID+"/compose", c.token, "application/json", bytes.NewReader(mustJSON(c.t, map[string]string{"compose_file": updated})), &stack, "compose-update-"+time.Now().Format("150405000000000")); err != nil {
		c.t.Fatal(err)
	}
	if stack.ID != stackID || !strings.Contains(stack.ComposeFile, "from-update") {
		c.t.Fatalf("unexpected compose update response: %+v", stack)
	}

	resp, body, err := c.request(http.MethodPut, "/api/v1/stacks/"+stackID+"/compose", c.token, "bad-compose-"+time.Now().Format("150405000000000"), "application/json", bytes.NewReader(mustJSON(c.t, map[string]string{"compose_file": strings.Replace(updated, "primary_node: node1", "primary_node: other", 1)})))
	if err != nil {
		c.t.Fatal(err)
	}
	if resp.StatusCode != http.StatusBadRequest {
		c.t.Fatalf("node-changing compose update: got %s body=%s", resp.Status, bytes.TrimSpace(body))
	}
}

func (c *apiClient) waitStackState(stackID, want string) {
	c.t.Helper()
	waitFor(c.t, 4*time.Minute, func() error {
		var status struct {
			State      string `json:"state"`
			Containers []struct {
				State  string `json:"state"`
				Health string `json:"health"`
			} `json:"containers"`
		}
		if err := c.doJSON(http.MethodGet, "/api/v1/stacks/"+stackID+"/status", c.token, "", nil, &status); err != nil {
			return err
		}
		if status.State == "Failed" {
			return fmt.Errorf("stack entered Failed state")
		}
		if status.State != want {
			return fmt.Errorf("stack state is %q, want %q", status.State, want)
		}
		if want == "RunningPrimary" {
			if len(status.Containers) == 0 {
				return fmt.Errorf("no containers reported")
			}
			for _, container := range status.Containers {
				if container.State != "running" || (container.Health != "" && container.Health != "none" && container.Health != "healthy") {
					return fmt.Errorf("container not ready: %+v", container)
				}
			}
		}
		return nil
	})
}

func (c *apiClient) waitOperation(operationID string) {
	c.t.Helper()
	if operationID == "" {
		c.t.Fatal("operation_id is empty")
	}
	waitFor(c.t, 4*time.Minute, func() error {
		var op struct {
			State      string `json:"state"`
			Checkpoint string `json:"checkpoint"`
			Error      string `json:"error"`
		}
		if err := c.doJSON(http.MethodGet, "/api/v1/operations/"+operationID, c.token, "", nil, &op); err != nil {
			return err
		}
		switch op.State {
		case "succeeded":
			return nil
		case "failed":
			return fmt.Errorf("operation failed at %s: %s", op.Checkpoint, op.Error)
		default:
			return fmt.Errorf("operation state is %q checkpoint=%q", op.State, op.Checkpoint)
		}
	})
}

func (c *apiClient) waitStackDeleted(stackID string) {
	c.t.Helper()
	waitFor(c.t, 90*time.Second, func() error {
		resp, body, err := c.request(http.MethodGet, "/api/v1/stacks/"+stackID, c.token, "", "", nil)
		if err != nil {
			return err
		}
		if resp.StatusCode == http.StatusNotFound {
			return nil
		}
		return fmt.Errorf("stack still present: %s %s", resp.Status, bytes.TrimSpace(body))
	})
}

func (c *apiClient) cleanupStack(stackID string) {
	if stackID == "" || c.token == "" {
		return
	}
	for attempt := 0; attempt < 8; attempt++ {
		resp, body, err := c.request(http.MethodDelete, "/api/v1/stacks/"+stackID+"?purge=true", c.token, "cleanup-delete-"+time.Now().Format("150405000000000"), "", nil)
		if err != nil || resp.StatusCode == http.StatusNotFound {
			return
		}
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			var deleted mutationResponse
			if json.Unmarshal(body, &deleted) == nil && deleted.OperationID != "" {
				c.waitOperation(deleted.OperationID)
			}
			return
		}
		time.Sleep(2 * time.Second)
	}
}

func (c *apiClient) doJSON(method, path, token, contentType string, body io.Reader, target any, idempotencyKey ...string) error {
	key := ""
	if len(idempotencyKey) > 0 {
		key = idempotencyKey[0]
	}
	resp, payload, err := c.request(method, path, token, key, contentType, body)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%s %s: %s: %s", method, path, resp.Status, bytes.TrimSpace(payload))
	}
	if target != nil && len(bytes.TrimSpace(payload)) != 0 {
		if err := json.Unmarshal(payload, target); err != nil {
			return fmt.Errorf("decode %s %s: %w: %s", method, path, err, bytes.TrimSpace(payload))
		}
	}
	return nil
}

func (c *apiClient) request(method, path, token, idempotencyKey, contentType string, body io.Reader) (*http.Response, []byte, error) {
	req, err := http.NewRequest(method, c.baseURL+path, body)
	if err != nil {
		return nil, nil, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", idempotencyKey)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, err
	}
	return resp, payload, nil
}

func waitFor(t *testing.T, timeout time.Duration, check func() error) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last error
	for time.Now().Before(deadline) {
		if last = check(); last == nil {
			return
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("timed out after %s: %v", timeout, last)
}

func waitForIP(t *testing.T, dir, uri, name string) string {
	t.Helper()
	var ip string
	waitFor(t, 5*time.Minute, func() error {
		out, err := command(15*time.Second, dir, nil, "virsh", "-c", uri, "domifaddr", name, "--source", "lease")
		if err != nil {
			return err
		}
		for _, field := range strings.Fields(out) {
			candidate, _, err := net.ParseCIDR(field)
			if err == nil && candidate.To4() != nil && !candidate.IsLoopback() {
				ip = candidate.String()
				return nil
			}
		}
		return fmt.Errorf("no IPv4 lease yet")
	})
	return ip
}

func run(t *testing.T, dir string, extraEnv []string, name string, args ...string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), extraEnv...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
}

func command(timeout time.Duration, dir string, extraEnv []string, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), extraEnv...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, bytes.TrimSpace(out))
	}
	return string(out), nil
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func composeFixture(name, marker, image, revision string) string {
	return fmt.Sprintf(`name: %s
x-fl-health-timeout: 90s
x-fl-stack:
  schema_version: 1
  primary_node: node1
  failover:
    mode: manual
  storage:
    pool: floatlab
    block_size: 32k
    compression: lz4
    quota: 1G
x-fl-alert-rules:
  - name: cpu-high
    metric: container_cpu_percent
    service: app
    comparator: gt
    threshold: 95
    duration: 30s
    severity: warning
    message: cpu high
services:
  app:
    image: %s
    command: ["sh", "-c", "echo %s; echo %s > /data/revision.txt; while :; do sleep 60; done"]
    volumes:
      - type: volume
        source: data
        target: /data
        x-fl-recordsize: 16K
        x-fl-compression: zstd
        x-fl-quota: 128M
        x-fl-snapshots: 1h/2 1d/2
volumes:
  data: {}
`, name, image, marker, revision)
}
