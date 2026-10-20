package hostupgrade

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/floatlab/floatlab-core/internal/update"
	"golang.org/x/sys/unix"
)

func TrialToken(cmdline string) (string, error) {
	token := ""
	for _, part := range strings.Fields(cmdline) {
		if strings.HasPrefix(part, "floatlab.trial=") {
			if token != "" {
				return "", fmt.Errorf("duplicate trial operation token")
			}
			token = strings.TrimPrefix(part, "floatlab.trial=")
			if !operationID.MatchString(token) {
				return "", fmt.Errorf("invalid trial operation token")
			}
		}
	}
	return token, nil
}

type guardMessage struct {
	OperationID string `json:"operation_id"`
	State       string `json:"state"`
	Nonce       int64  `json:"nonce"`
}

func WriteGuardMessage(operation, state string) error {
	if !operationID.MatchString(operation) {
		return fmt.Errorf("invalid operation id")
	}
	return update.WriteJSON(filepath.Join("/run/floatlab-boot-recovery", operation+".json"), guardMessage{operation, state, time.Now().UnixNano()})
}

// GuardState uses process-monotonic elapsed time; wall clock changes cannot postpone recovery.
type GuardState struct {
	OperationID   string
	Started       time.Time
	LastHeartbeat time.Time
	LastNonce     int64
	Ready         bool
}

func (g *GuardState) Observe(now time.Time, operation, state string, nonce int64) {
	if operation != g.OperationID || state != "ready" || nonce == g.LastNonce {
		return
	}
	g.Ready = true
	g.LastHeartbeat = now
	g.LastNonce = nonce
}
func (g GuardState) Expired(now time.Time) bool {
	if !g.Ready {
		return now.Sub(g.Started) >= 10*time.Minute
	}
	return now.Sub(g.LastHeartbeat) >= time.Minute
}

// Guard needs neither the persistent mounts nor hostd, networking, or Docker to arm.
func Guard(ctx context.Context) error {
	b, err := os.ReadFile("/proc/cmdline")
	if err != nil {
		return err
	}
	token, err := TrialToken(string(b))
	if err != nil {
		return err
	}
	if token == "" {
		return nil
	}
	g := GuardState{OperationID: token, Started: time.Now()}
	// Persist CLOCK_BOOTTIME offsets in /run so a guard crash cannot renew its deadline.
	var clock unix.Timespec
	if err = unix.ClockGettime(unix.CLOCK_BOOTTIME, &clock); err != nil {
		return err
	}
	bootNow := clock.Nano()
	type savedGuard struct {
		Started   int64 `json:"started"`
		Heartbeat int64 `json:"heartbeat"`
		Nonce     int64 `json:"nonce"`
		Ready     bool  `json:"ready"`
	}
	statePath := filepath.Join("/run/floatlab-boot-recovery", token+"-guard.json")
	saved := savedGuard{Started: bootNow}
	if b, e := os.ReadFile(statePath); e == nil {
		if e = json.Unmarshal(b, &saved); e != nil {
			return e
		}
		if saved.Started <= 0 || saved.Started > bootNow {
			return fmt.Errorf("invalid guard monotonic checkpoint")
		}
		g.Started = time.Now().Add(-time.Duration(bootNow - saved.Started))
		g.Ready, g.LastNonce = saved.Ready, saved.Nonce
		if saved.Ready {
			g.LastHeartbeat = time.Now().Add(-time.Duration(bootNow - saved.Heartbeat))
		}
	} else if !os.IsNotExist(e) {
		return e
	} else if err = update.WriteJSON(statePath, saved); err != nil {
		return err
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case now := <-ticker.C:
			b, e := os.ReadFile(filepath.Join("/run/floatlab-boot-recovery", token+".json"))
			if e == nil {
				var message guardMessage
				if json.Unmarshal(b, &message) == nil && message.OperationID == token {
					if message.State == "committed" {
						s, e := NewDefault().Status()
						if e == nil && s.OperationID == token && s.State == "committed" && s.CommitFinalized {
							return nil
						}
					}
					g.Observe(now, message.OperationID, message.State, message.Nonce)
					if saved.Nonce != g.LastNonce {
						if e = unix.ClockGettime(unix.CLOCK_BOOTTIME, &clock); e != nil {
							return e
						}
						saved.Heartbeat, saved.Nonce, saved.Ready = clock.Nano(), g.LastNonce, g.Ready
						if e = update.WriteJSON(statePath, saved); e != nil {
							return e
						}
					}
				}
			}
			if g.Expired(now) {
				_ = update.WriteJSON(filepath.Join(Directory, "boot-failure-"+token+".json"), map[string]any{"operation_id": token, "error": "trial readiness deadline or heartbeat expired", "time": time.Now().UTC()})
				reboot, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				_ = (Linux{}).run(reboot, "systemctl", "reboot")
				cancel()
				// If orderly shutdown stalls, PID1's bounded force request still runs independently.
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(30 * time.Second):
				}
				return (Linux{}).run(context.Background(), "systemctl", "reboot", "--force", "--force")
			}
		}
	}
}
