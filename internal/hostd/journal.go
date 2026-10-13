package hostd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/floatlab/floatlab-core/internal/credentials"
	"github.com/floatlab/floatlab-core/pkg/logs"
	"go.uber.org/zap"
)

const journalCursorPath = "/run/floatlab/journal.cursor"

func (s *Server) forwardJournal(ctx context.Context) {
	client := logs.NewClient(telemetryEnv("FLOATLAB_VLOGS_URL", "http://127.0.0.1:9428"))
	for ctx.Err() == nil {
		err := followJournal(ctx, client, telemetryEnv("FLOATLAB_NODE_ID", "node1"), journalCursorPath)
		if ctx.Err() != nil {
			return
		}
		s.log.Warn("hostd: journal forwarding interrupted; retrying", zap.Error(err))
		timer := time.NewTimer(5 * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func followJournal(ctx context.Context, client *logs.Client, nodeID, cursorPath string) error {
	args := []string{"--boot", "--follow", "--no-tail", "--no-pager", "--all", "--output=json"}
	cursor, err := os.ReadFile(cursorPath)
	if err == nil {
		args = append(args, "--after-cursor="+strings.TrimSpace(string(cursor)))
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(ctx, "journalctl", args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	defer func() {
		cancel()
		_ = cmd.Wait()
	}()
	return forwardJournalStream(ctx, stdout, client, nodeID, cursorPath)
}

func forwardJournalStream(ctx context.Context, reader io.Reader, client *logs.Client, nodeID, cursorPath string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	type result struct {
		entry map[string]json.RawMessage
		err   error
	}
	entries := make(chan result, 128)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		decoder := json.NewDecoder(reader)
		for {
			var next result
			next.err = decoder.Decode(&next.entry)
			select {
			case entries <- next:
			case <-ctx.Done():
				return
			}
			if next.err != nil {
				return
			}
		}
	}()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	batch := make([]map[string]string, 0, 128)
	var cursor string
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		if err := client.Push(ctx, batch); err != nil {
			return err
		}
		// Checkpoint only acknowledged batches; retries may duplicate, never skip.
		if err := credentials.WriteBytes(cursorPath, []byte(cursor)); err != nil {
			return err
		}
		batch = batch[:0]
		return nil
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := flush(); err != nil {
				return err
			}
		case next := <-entries:
			if next.err != nil {
				if err := flush(); err != nil {
					return err
				}
				return next.err
			}
			line, err := journalLine(next.entry, nodeID)
			if err != nil {
				return err
			}
			cursor = journalText(next.entry["__CURSOR"])
			if cursor == "" {
				return fmt.Errorf("journal entry has no cursor")
			}
			batch = append(batch, line)
			if len(batch) == cap(batch) {
				if err := flush(); err != nil {
					return err
				}
			}
		}
	}
}

func journalLine(entry map[string]json.RawMessage, nodeID string) (map[string]string, error) {
	micros, err := strconv.ParseInt(journalText(entry["__REALTIME_TIMESTAMP"]), 10, 64)
	if err != nil {
		return nil, fmt.Errorf("journal timestamp: %w", err)
	}
	line := map[string]string{
		"_time":   time.UnixMicro(micros).UTC().Format(time.RFC3339Nano),
		"_msg":    journalText(entry["MESSAGE"]),
		"node_id": nodeID,
		"source":  "journal",
	}
	for field, name := range map[string]string{
		"_HOSTNAME": "host", "_SYSTEMD_UNIT": "service", "SYSLOG_IDENTIFIER": "identifier",
		"PRIORITY": "priority", "CONTAINER_ID_FULL": "container_id", "CONTAINER_NAME": "container_name",
	} {
		if value := journalText(entry[field]); value != "" {
			line[name] = value
		}
	}
	if journalText(entry["_TRANSPORT"]) == "kernel" {
		line["source"] = "kernel"
	} else if line["container_id"] != "" {
		line["source"] = "container"
	}
	return line, nil
}

func journalText(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var data []byte
	if json.Unmarshal(raw, &data) == nil {
		return string(data)
	}
	var values []string
	if json.Unmarshal(raw, &values) == nil {
		return strings.Join(values, "\n")
	}
	return ""
}
