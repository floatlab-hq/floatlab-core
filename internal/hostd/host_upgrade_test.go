package hostd

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/floatlab/floatlab-core/internal/update"
)

func TestStartupWaitsForCoreRecoveryBeforeReadingTrialForSupervision(t *testing.T) {
	dir := t.TempDir()
	unlock, err := update.Acquire(dir)
	if err != nil {
		t.Fatal(err)
	}
	unlocked := false
	defer func() {
		if !unlocked {
			unlock()
		}
	}()
	path := filepath.Join(dir, "host.json")
	if err = update.WriteJSON(path, map[string]string{"state": "armed"}); err != nil {
		t.Fatal(err)
	}
	attempted := make(chan struct{}, 1)
	finished := make(chan error, 1)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	go func() {
		finished <- waitForUpdateRecovery(ctx, func(context.Context) error {
			release, e := update.Acquire(dir)
			if e != nil {
				select {
				case attempted <- struct{}{}:
				default:
				}
				return e
			}
			defer release()
			b, e := os.ReadFile(path)
			if e != nil {
				return e
			}
			var status struct {
				State string `json:"state"`
			}
			if e = json.Unmarshal(b, &status); e != nil {
				return e
			}
			if status.State != "trial" {
				return errors.New("supervisor would have exited before core recovery saved trial")
			}
			return nil
		})
	}()
	<-attempted
	if err = update.WriteJSON(path, map[string]string{"state": "trial"}); err != nil {
		t.Fatal(err)
	}
	unlock()
	unlocked = true
	if err = <-finished; err != nil {
		t.Fatal(err)
	}
}

func TestStartupRecoveryDoesNotRetryRealFailureOrIgnoreCancellation(t *testing.T) {
	failure := errors.New("restore failed")
	calls := 0
	if err := waitForUpdateRecovery(context.Background(), func(context.Context) error { calls++; return failure }); !errors.Is(err, failure) || calls != 1 {
		t.Fatalf("error=%v calls=%d", err, calls)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := waitForUpdateRecovery(ctx, func(context.Context) error { t.Fatal("called recovery after cancellation"); return nil }); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
