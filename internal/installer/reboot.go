package installer

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// Export cleanly so the next boot can import using its unique host ID without force.
func reboot(ctx context.Context) error {
	if err := run(ctx, "systemctl", "stop", "floatlab-core-stack.service", "docker.service", "docker.socket", "floatlab-hostd.service"); err != nil {
		return err
	}
	pools, err := exec.CommandContext(ctx, "zpool", "list", "-H", "-o", "name").CombinedOutput()
	if err != nil && !strings.Contains(string(pools), "no pools available") {
		return fmt.Errorf("cannot inspect storage before reboot: %w: %s", err, Diagnostic(pools))
	}
	if strings.Contains("\n"+strings.TrimSpace(string(pools))+"\n", "\nfloatlab\n") {
		if err := run(ctx, "zpool", "export", "floatlab"); err != nil {
			return fmt.Errorf("cannot safely export storage before reboot: %w", err)
		}
	}
	return run(ctx, "systemctl", "reboot")
}
