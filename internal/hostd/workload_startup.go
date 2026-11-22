package hostd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/floatlab/floatlab-core/internal/hostnetwork"
	"github.com/floatlab/floatlab-core/pkg/ipc"
)

func dockerStartupGuard(ctx context.Context, _ json.RawMessage) (any, error) {
	value, err := runShell(ctx, "systemctl", "show", "--property=MainPID", "--value", "docker.service")
	if err != nil {
		return nil, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || pid <= 0 {
		return nil, errors.New("Docker service has no running process")
	}
	args, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil {
		return nil, err
	}
	return strings.Contains("\x00"+string(args), "\x00--restart=false\x00"), nil
}

func (d *Dispatcher) resumeStackOnStartup(ctx context.Context, raw json.RawMessage) (any, error) {
	if d.docker == nil {
		return nil, errors.New("Docker client not available")
	}
	var p ipc.DockerListPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	if !composeProject.MatchString(p.StackID) {
		return nil, errors.New("invalid stack ID")
	}
	return d.docker.ResumeStack(ctx, p.StackID)
}

func probeWorkloadAddress(ctx context.Context, raw json.RawMessage) (any, error) {
	var p ipc.WorkloadAddressProbe
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	ip, err := netip.ParseAddr(p.Address)
	if err != nil || ip.Zone() != "" || !ip.IsGlobalUnicast() {
		return nil, errors.New("invalid workload address")
	}
	if p.DockerNetwork != "" {
		value, err := runShell(ctx, "docker", "network", "inspect", "--format", `{{.Driver}} {{index .Options "parent"}}`, "--", p.DockerNetwork)
		if err != nil {
			return nil, err
		}
		driver, parent, _ := strings.Cut(strings.TrimSpace(value), " ")
		if driver != "macvlan" && driver != "ipvlan" {
			return false, nil
		}
		if parent == "" {
			return nil, errors.New("physical Docker network has no parent interface")
		}
		p.Interface = parent
	}
	if p.Interface == "" {
		p.Interface = hostnetwork.Bridge
	}
	iface, err := net.InterfaceByName(p.Interface)
	if err != nil {
		return nil, err
	}
	if iface.Flags&net.FlagUp == 0 || len(iface.HardwareAddr) != 6 {
		return nil, fmt.Errorf("workload interface %s is not up with an Ethernet MAC", p.Interface)
	}
	if ip.Is4() {
		err = exec.CommandContext(ctx, "arping", "-q", "-D", "-c", "2", "-w", "2", "-I", p.Interface, ip.String()).Run()
		if err == nil {
			return false, nil
		}
		var exit *exec.ExitError
		if ctx.Err() == nil && errors.As(err, &exit) && exit.ExitCode() == 1 {
			return true, nil
		}
	} else {
		err = exec.CommandContext(ctx, "ndisc6", "-q", "-r", "2", "-w", "1000", ip.String(), p.Interface).Run()
		if err == nil {
			return true, nil
		}
		var exit *exec.ExitError
		if ctx.Err() == nil && errors.As(err, &exit) && exit.ExitCode() == 2 {
			return false, nil
		}
	}
	return nil, fmt.Errorf("probe workload address %s on %s: %w", ip, p.Interface, err)
}
