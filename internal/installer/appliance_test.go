//go:build appliance

package installer

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"testing"
	"time"

	"github.com/floatlab/floatlab-core/internal/hostnetwork"
)

// Run only inside the disposable UEFI guest; the serial is assigned by its harness.
func TestApplianceInstall(t *testing.T) {
	if os.Getenv("FLOATLAB_APPLIANCE_TEST") != "1" {
		t.Skip("requires disposable appliance")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	input, requests := io.Pipe()
	events, output := io.Pipe()
	defer requests.Close()
	defer events.Close()
	go func() { output.CloseWithError((&Service{}).Serve(ctx, input, output)) }()
	encoder, decoder := json.NewEncoder(requests), json.NewDecoder(events)
	send := func(request Request) {
		t.Helper()
		if err := encoder.Encode(request); err != nil {
			t.Fatal(err)
		}
	}
	for {
		var event Event
		if err := decoder.Decode(&event); err != nil {
			t.Fatal(err)
		}
		if event.Error != "" || event.Status != nil && event.Status.Error != "" {
			t.Fatalf("installer: %+v", event)
		}
		switch event.Type {
		case "inventory":
			var selected []Disk
			for _, disk := range event.Disks {
				if disk.Serial == "floatlab-upgrade" && disk.Eligible {
					selected = append(selected, disk)
				}
			}
			if len(selected) != 1 {
				t.Fatalf("expected one disposable upgrade disk: %+v", event.Disks)
			}
			send(Request{Type: "preview", Command: ZPoolCommand(selected)})
		case "preview":
			send(Request{Type: "confirm", Command: event.Command, Confirmation: "ERASE ALL DATA"})
		case "confirm":
			send(Request{Type: "configure", Config: &InstallConfig{NodeName: "upgrade-appliance", Timezone: "UTC", Network: hostnetwork.DefaultConfig(), Username: "admin", Password: "test-password", PasswordConfirmation: "test-password"}})
		case "status":
			t.Logf("%s: %s", event.Status.Phase, event.Status.Activity)
		case "failed":
			t.Fatalf("installation failed: %+v", event.Status)
		case "complete":
			if !event.Status.Ready {
				t.Fatal("installer completed without readiness")
			}
			return
		}
	}
}
