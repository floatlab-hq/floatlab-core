package hostd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/floatlab/floatlab-core/pkg/logs"
)

func TestJournalForwardingCheckpointsOnlyAcknowledgedBatches(t *testing.T) {
	var input strings.Builder
	for i := range 130 {
		fmt.Fprintf(&input, `{"__CURSOR":"cursor-%d","__REALTIME_TIMESTAMP":"1000001","MESSAGE":"hello","_HOSTNAME":"appliance","_SYSTEMD_UNIT":"docker.service","CONTAINER_ID_FULL":"container-id","CONTAINER_NAME":"control"}`+"\n", i)
	}
	var batches []int
	fail := true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/insert/jsonline" || r.Header.Get("Content-Type") != "application/stream+json" {
			t.Errorf("unexpected request: %s %s", r.URL, r.Header)
		}
		decoder := json.NewDecoder(r.Body)
		count := 0
		for {
			var line map[string]string
			if err := decoder.Decode(&line); errors.Is(err, io.EOF) {
				break
			} else if err != nil {
				t.Error(err)
				break
			}
			count++
			if line["_time"] != "1970-01-01T00:00:01.000001Z" || line["_msg"] != "hello" || line["source"] != "container" || line["node_id"] != "node-test" || line["container_id"] != "container-id" {
				t.Errorf("unexpected forwarded entry: %#v", line)
			}
		}
		batches = append(batches, count)
		if fail && len(batches) == 2 {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	defer server.Close()
	cursorPath := filepath.Join(t.TempDir(), "journal.cursor")
	client := logs.NewClient(server.URL)
	if err := forwardJournalStream(t.Context(), strings.NewReader(input.String()), client, "node-test", cursorPath); err == nil || errors.Is(err, io.EOF) {
		t.Fatalf("expected failed delivery, got %v", err)
	}
	cursor, err := os.ReadFile(cursorPath)
	if err != nil || string(cursor) != "cursor-127" {
		t.Fatalf("unacknowledged entries advanced cursor: %q, %v", cursor, err)
	}
	fail = false
	retry := strings.NewReader(strings.Join(strings.Split(input.String(), "\n")[128:], "\n"))
	if err := forwardJournalStream(t.Context(), retry, client, "node-test", cursorPath); !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	cursor, err = os.ReadFile(cursorPath)
	if err != nil || string(cursor) != "cursor-129" {
		t.Fatalf("acknowledged retry not checkpointed: %q, %v", cursor, err)
	}
	if fmt.Sprint(batches) != "[128 2 2]" {
		t.Fatalf("unexpected batches: %v", batches)
	}
}

func TestJournalLineHandlesKernelAndBinaryMessages(t *testing.T) {
	var entry map[string]json.RawMessage
	if err := json.Unmarshal([]byte(`{"__REALTIME_TIMESTAMP":"1000000","MESSAGE":[104,105,0,255],"_TRANSPORT":"kernel","PRIORITY":"3"}`), &entry); err != nil {
		t.Fatal(err)
	}
	line, err := journalLine(entry, "node-test")
	if err != nil || line["_msg"] != "hi\x00\xff" || line["source"] != "kernel" || line["priority"] != "3" {
		t.Fatalf("unexpected kernel entry: %#v, %v", line, err)
	}
	entry["__REALTIME_TIMESTAMP"] = json.RawMessage(`"invalid"`)
	if _, err := journalLine(entry, "node-test"); err == nil {
		t.Fatal("accepted invalid timestamp")
	}
	if got := journalText(json.RawMessage(`["one","two"]`)); got != "one\ntwo" {
		t.Fatalf("repeated journal field: %q", got)
	}
}

func TestJournalForwardingStopsOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := forwardJournalStream(ctx, strings.NewReader(""), logs.NewClient("http://unused"), "node", filepath.Join(t.TempDir(), "cursor")); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
}

func TestFollowJournalResumesCurrentBootAfterSavedCursor(t *testing.T) {
	directory := t.TempDir()
	argsPath := filepath.Join(directory, "args")
	t.Setenv("JOURNAL_ARGS_PATH", argsPath)
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$JOURNAL_ARGS_PATH\"\nprintf '%s\\n' '{\"__CURSOR\":\"next\",\"__REALTIME_TIMESTAMP\":\"1000000\",\"MESSAGE\":\"boot log\"}'\n"
	if err := os.WriteFile(filepath.Join(directory, "journalctl"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	cursorPath := filepath.Join(directory, "cursor")
	if err := os.WriteFile(cursorPath, []byte("saved"), 0600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	if err := followJournal(t.Context(), logs.NewClient(server.URL), "node", cursorPath); !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	args, err := os.ReadFile(argsPath)
	if err != nil || !strings.Contains(string(args), "--boot\n") || !strings.Contains(string(args), "--no-tail\n") || !strings.Contains(string(args), "--after-cursor=saved\n") {
		t.Fatalf("unexpected journalctl arguments: %q, %v", args, err)
	}
	cursor, err := os.ReadFile(cursorPath)
	if err != nil || string(cursor) != "next" {
		t.Fatalf("cursor not updated: %q, %v", cursor, err)
	}
}
