package integration_test

import (
	"context"
	"fmt"
	"strings"
	"time"
)

func runAutomaticUpgradeRollback(t testT, c *apiClient) {
	name := fmt.Sprintf("auto-upgrade-%d", time.Now().UnixNano())
	registry := name + "-registry"
	guest := func(script string) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
		defer cancel()
		out, err := c.vm.Exec(ctx, "set -eu; "+script)
		if err != nil {
			t.Fatalf("appliance command failed: %s: %v", out, err)
		}
		return strings.TrimSpace(out)
	}
	advanceDay := func() string {
		t.Helper()
		day := guest(`date -u -s 'tomorrow 12:00:00' '+%F'`)
		c.login()
		return day
	}
	guest("systemctl stop systemd-timesyncd.service")
	advanceDay()
	guest("docker run -d --name " + registry + " -p 127.0.0.1::5000 registry:2 >/dev/null")
	t.Cleanup(func() { guest("docker rm -f " + registry) })
	address := guest("docker port " + registry + " 5000/tcp")
	repo := address + "/upgrade-test"
	publish := func(version string) {
		t.Helper()
		image := repo + ":" + version
		guest(fmt.Sprintf(`fixtureFile=$(mktemp); fixtureContainer=$(docker create alpine:3.20)
trap 'rm -f "$fixtureFile"; docker rm "$fixtureContainer" >/dev/null' EXIT
printf '%%s\n' '%s' >"$fixtureFile"
docker cp "$fixtureFile" "$fixtureContainer":/fixture-version
docker commit "$fixtureContainer" %s
docker push %s`, version, image, image))
	}
	publish("1.0.0")
	guest("docker tag " + repo + ":1.0.0 " + repo + ":latest; docker push " + repo + ":latest")
	source := fmt.Sprintf(`name: %s
x-fl-health-timeout: 60s
x-fl-stack:
  primary_node: %s
  storage: {pool: floatlab}
services:
  app:
    image: %s:latest
    command:
      - sh
      - -ec
      - |
        case "$(cat /fixture-version)" in
          1.0.0) test -e /data/value || printf original >/data/value ;;
          1.0.1) printf corrupted >/data/value ;;
          1.0.2) test "$(cat /data/value)" = original; touch /data/upgraded ;;
        esac
        touch /tmp/ready
        while :; do sleep 30; done
    healthcheck:
      test: ["CMD-SHELL", "test -f /tmp/ready && ! grep -Fxq 1.0.1 /fixture-version"]
      interval: 1s
      retries: 20
    volumes: ["data:/data"]
volumes:
  data: {}
`, name, c.nodeID, repo)
	created := c.object("POST", "/api/v1/stacks", map[string]string{"name": name, "compose_file": source}, 201)
	id := created["id"].(string)
	t.Cleanup(func() { c.cleanupStack(id) })
	path := "/api/v1/stacks/" + id
	if !strings.Contains(created["compose_file"].(string), repo+":1.0.0@sha256:") {
		t.Fatalf("saved Compose did not pin the initial release: %s", created["compose_file"])
	}
	c.waitStackState(id, "Idle")
	c.waitOperation(c.mutate("POST", path+"/start", nil, "application/json").OperationID)
	c.waitStackState(id, "RunningPrimary")
	image := func() string {
		return guest("docker inspect --format '{{.Image}} {{.Config.Image}}' $(docker ps -q --filter label=com.docker.compose.project=" + id + ")")
	}
	initialImage := image()
	datasetName := "floatlab/" + name + "/data"
	dataset := "/" + datasetName
	if got := guest("findmnt -n -o SOURCE --target " + dataset); got != datasetName {
		t.Fatalf("expected the workload's data on ZFS dataset %s, got %s", datasetName, got)
	}
	if got := guest("cat " + dataset + "/value"); got != "original" {
		t.Fatalf("initial data = %q", got)
	}
	publish("1.0.1")
	const settings = "/api/v1/settings/workload-upgrades"
	c.expect("GET", settings, nil, 200)
	c.object("PUT", settings, map[string]string{"window": "00:00-23:59"}, 200)
	autoEvents := func() []map[string]any {
		var upgrades []map[string]any
		for _, value := range c.object("GET", path+"/events", nil, 200)["items"].([]any) {
			event := value.(map[string]any)
			if event["type"] == "Upgrade" && event["actor"] == "automatic-upgrade" {
				upgrades = append(upgrades, event)
			}
		}
		return upgrades
	}
	waitFor(t, 3*time.Minute, func() error {
		out, err := guestCommand(c.vm, "cat "+dataset+"/value")
		if err != nil || strings.TrimSpace(out) != "corrupted" {
			return fmt.Errorf("waiting for the failing image to write to ZFS: %q %v", out, err)
		}
		return nil
	})
	t.Log("Observed the 1.0.1 candidate overwrite the ZFS data")
	var failedOperation string
	waitFor(t, 3*time.Minute, func() error {
		events := autoEvents()
		if len(events) == 0 {
			return fmt.Errorf("waiting for the first scheduled upgrade")
		}
		if len(events) != 1 || events[0]["outcome"] != "failed-rolled-back" {
			t.Fatalf("expected one failed and rolled-back upgrade: %+v", events)
		}
		failedOperation = events[0]["operation_id"].(string)
		operation := c.object("GET", "/api/v1/operations/"+failedOperation, nil, 200)
		if operation["state"] != "failed" || operation["checkpoint"] != "rolled-back" {
			return fmt.Errorf("waiting for failed upgrade completion: %+v", operation)
		}
		return nil
	})
	c.waitStackState(id, "RunningPrimary")
	if got := image(); got != initialImage {
		t.Fatalf("rollback image = %s, want exact previous image %s", got, initialImage)
	}
	if got := guest("cat " + dataset + "/value"); got != "original" {
		t.Fatalf("rollback data = %q, want original", got)
	}
	alerts := c.expect("GET", "/api/v1/alerts?stack_id="+id+"&state=active", nil, 200).([]any)
	if len(alerts) != 1 || alerts[0].(map[string]any)["rule_id"] != "workload-upgrade-failed:"+failedOperation {
		t.Fatalf("expected a live alert for the failed upgrade: %+v", alerts)
	}
	t.Logf("Rollback restored %s and the original ZFS bytes. Alert recorded for %s", initialImage, failedOperation)

	day := advanceDay()
	waitFor(t, 3*time.Minute, func() error {
		query := fmt.Sprintf(`docker exec $(docker ps -q --filter label=com.docker.compose.service=floatlab-control) sqlite3 -readonly /floatlab/system/workloads/local.sqlite "SELECT last_run_at,(SELECT count(*) FROM operations WHERE stack_id='%s' AND action='upgrade' AND actor='automatic-upgrade') FROM scheduler_state WHERE stack_id='%s' AND tier='image-upgrade'"`, id, id)
		out := guest(query)
		checkedDay, attempts, _ := strings.Cut(out, "|")
		if checkedDay != day {
			return fmt.Errorf("waiting for the following day's scheduler check, last run %q", out)
		}
		if attempts != "1" {
			t.Fatalf("unchanged release triggered another upgrade operation: %s", out)
		}
		return nil
	})
	if events, got := autoEvents(), image(); len(events) != 1 || got != initialImage {
		t.Fatalf("same release was retried: image=%s events=%+v", got, events)
	}
	t.Log("The next day's completed check skipped the unchanged 1.0.1 candidate")

	publish("1.0.2")
	advanceDay()
	waitFor(t, 3*time.Minute, func() error {
		events := autoEvents()
		if len(events) == 1 {
			return fmt.Errorf("waiting for scheduled upgrade of 1.0.2")
		}
		if len(events) != 2 || events[1]["outcome"] != "succeeded" {
			t.Fatalf("new release did not upgrade successfully: %+v", events)
		}
		return nil
	})
	c.waitStackState(id, "RunningPrimary")
	if got := image(); !strings.Contains(got, repo+":1.0.2@sha256:") {
		t.Fatalf("new release did not deploy by digest: %s", got)
	}
	if got := guest("test -f " + dataset + "/upgraded; cat " + dataset + "/value"); got != "original" {
		t.Fatalf("new release did not preserve original data: %q", got)
	}
	t.Log("Scheduled upgrade installed 1.0.2 and preserved the restored ZFS data")
}
