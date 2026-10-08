package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/floatlab/floatlab-core/pkg/logs"
	"github.com/google/uuid"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	dockerclient "github.com/moby/moby/client"
	"go.uber.org/zap"
)

// ForwardTelemetry collects Docker samples and logs for FloatLab workloads.
// ponytail: poll every five seconds, tail capped at 1000 lines/1 MiB per container;
// use continuous log streams if workloads exceed this polling budget.
func (c *Client) ForwardTelemetry(ctx context.Context, nodeID, logsURL, metricsURL string, log *zap.Logger) {
	checkpoints := map[string]time.Time{}
	client := &http.Client{Timeout: 10 * time.Second}
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		c.collectTelemetry(ctx, nodeID, logsURL, metricsURL, client, checkpoints, log)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (c *Client) collectTelemetry(ctx context.Context, nodeID, logsURL, metricsURL string, client *http.Client, checkpoints map[string]time.Time, log *zap.Logger) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	listed, err := c.dc.ContainerList(ctx, dockerclient.ContainerListOptions{All: true, Filters: make(dockerclient.Filters).Add("label", LabelComposeProject)})
	if err != nil {
		log.Warn("telemetry: list containers", zap.Error(err))
		return
	}
	live := map[string]bool{}
	for _, item := range listed.Items {
		stackID := item.Labels[LabelComposeProject]
		if uuid.Validate(stackID) != nil {
			continue
		}
		live[item.ID] = true
		if item.State == container.StateRunning {
			sample, err := c.dc.ContainerStats(ctx, item.ID, dockerclient.ContainerStatsOptions{})
			if err == nil {
				var stats container.StatsResponse
				err = json.NewDecoder(sample.Body).Decode(&stats)
				sample.Body.Close()
				if err == nil {
					err = pushMetrics(ctx, client, metricsURL, dockerMetrics(stats, stackID, nodeID))
				}
			}
			if err != nil {
				log.Warn("telemetry: container metrics", zap.String("container", item.ID), zap.Error(err))
			}
		}
		inspected, err := c.dc.ContainerInspect(ctx, item.ID, dockerclient.ContainerInspectOptions{})
		if err != nil || inspected.Container.Config == nil {
			continue
		}
		since := checkpoints[item.ID]
		if since.IsZero() {
			since = time.Now().Add(-time.Minute)
		}
		until := time.Now().UTC()
		reader, err := c.dc.ContainerLogs(ctx, item.ID, dockerclient.ContainerLogsOptions{ShowStdout: true, ShowStderr: true, Timestamps: true, Since: since.Format(time.RFC3339Nano), Until: until.Format(time.RFC3339Nano), Tail: "1000"})
		if err != nil {
			log.Warn("telemetry: container logs", zap.Error(err))
			continue
		}
		var stdout, stderr bytes.Buffer
		remaining := ExecOutputLimit
		if inspected.Container.Config.Tty {
			_, err = io.Copy(&execWriter{&stdout, &remaining}, reader)
		} else {
			_, err = stdcopy.StdCopy(&execWriter{&stdout, &remaining}, &execWriter{&stderr, &remaining}, reader)
		}
		reader.Close()
		if err != nil {
			log.Warn("telemetry: read logs", zap.Error(err))
			continue
		}
		lines := dockerLogLines(stdout.String(), "stdout", item.ID, firstContainerName(item.Names), stackID, nodeID, item.Labels["com.docker.compose.service"])
		lines = append(lines, dockerLogLines(stderr.String(), "stderr", item.ID, firstContainerName(item.Names), stackID, nodeID, item.Labels["com.docker.compose.service"])...)
		if len(lines) > 0 {
			err = logs.NewClient(logsURL).Push(ctx, lines)
		}
		if err != nil {
			log.Warn("telemetry: push logs", zap.Error(err))
			continue
		}
		checkpoints[item.ID] = until
	}
	for id := range checkpoints {
		if !live[id] {
			delete(checkpoints, id)
		}
	}
}

func dockerLogLines(data, stream, id, name, stackID, nodeID, service string) []map[string]string {
	lines := []map[string]string{}
	for _, line := range strings.Split(strings.TrimSuffix(data, "\n"), "\n") {
		timestamp, message, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		lines = append(lines, map[string]string{"_time": timestamp, "_msg": message, "stream": stream, "container_id": id, "container_name": name, "stack_id": stackID, "node_id": nodeID, "service": service})
	}
	return lines
}

func dockerMetrics(stats container.StatsResponse, stackID, nodeID string) string {
	labels := fmt.Sprintf(`{stack_id=%s,container_id=%s,node_id=%s}`, strconv.Quote(stackID), strconv.Quote(stats.ID), strconv.Quote(nodeID))
	var body strings.Builder
	metric := func(name string, value float64) { fmt.Fprintf(&body, "%s%s %g\n", name, labels, value) }
	metric("container_cpu_usage_seconds_total", float64(stats.CPUStats.CPUUsage.TotalUsage)/1e9)
	metric("container_memory_usage_bytes", float64(stats.MemoryStats.Usage))
	var rx, tx uint64
	for _, network := range stats.Networks {
		rx += network.RxBytes
		tx += network.TxBytes
	}
	metric("container_network_receive_bytes_total", float64(rx))
	metric("container_network_transmit_bytes_total", float64(tx))
	for _, entry := range stats.BlkioStats.IoServiceBytesRecursive {
		if entry.Op != "Read" && entry.Op != "Write" {
			continue
		}
		fmt.Fprintf(&body, "container_blkio_device_usage_total{stack_id=%s,container_id=%s,node_id=%s,op=%s,device=%s} %d\n", strconv.Quote(stackID), strconv.Quote(stats.ID), strconv.Quote(nodeID), strconv.Quote(entry.Op), strconv.Quote(fmt.Sprintf("%d:%d", entry.Major, entry.Minor)), entry.Value)
	}
	return body.String()
}

func pushMetrics(ctx context.Context, client *http.Client, baseURL, body string) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(baseURL, "/")+"/api/v1/import/prometheus", strings.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "text/plain; version=0.0.4")
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	io.Copy(io.Discard, response.Body)
	if response.StatusCode >= 300 {
		return fmt.Errorf("metrics push: %s", response.Status)
	}
	return nil
}
