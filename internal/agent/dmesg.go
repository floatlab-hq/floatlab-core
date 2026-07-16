package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"time"
)

type DmesgForwarder struct {
	endpoint string
	client   *http.Client
	logger   *slog.Logger
	hostname string
}

func NewDmesgForwarder(baseURL string, logger *slog.Logger) (*DmesgForwarder, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return nil, err
	}
	parsed.Path = "/insert/jsonline"
	query := parsed.Query()
	query.Set("_stream_fields", "host,source")
	query.Set("_msg_field", "message")
	query.Set("_time_field", "timestamp")
	parsed.RawQuery = query.Encode()

	hostname, err := os.Hostname()
	if err != nil {
		hostname = "unknown"
	}

	return &DmesgForwarder{
		endpoint: parsed.String(),
		client:   &http.Client{Timeout: 5 * time.Second},
		logger:   logger,
		hostname: hostname,
	}, nil
}

func (f *DmesgForwarder) Run(ctx context.Context) {
	for {
		if err := f.followOnce(ctx); err != nil && ctx.Err() == nil {
			f.logger.Warn("dmesg forwarder stopped", "err", err)
			timer := time.NewTimer(5 * time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			continue
		}
		return
	}
}

func (f *DmesgForwarder) followOnce(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, "dmesg", "--follow", "--decode", "--ctime")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() {
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			f.logger.Warn("dmesg stderr", "line", scanner.Text())
		}
	}()

	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		if err := f.forwardLine(ctx, scanner.Text()); err != nil {
			f.logger.Warn("failed to forward dmesg line", "err", err)
		}
	}
	if err := scanner.Err(); err != nil {
		_ = cmd.Wait()
		return err
	}
	return cmd.Wait()
}

func (f *DmesgForwarder) forwardLine(ctx context.Context, message string) error {
	payload := map[string]string{
		"timestamp": time.Now().UTC().Format(time.RFC3339Nano),
		"host":      f.hostname,
		"source":    "kernel-dmesg",
		"message":   message,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	body = append(body, '\n')

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/stream+json")

	resp, err := f.client.Do(req)
	if err != nil {
		return err
	}
	defer drainAndClose(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("victoria logs returned %s", resp.Status)
	}
	return nil
}
