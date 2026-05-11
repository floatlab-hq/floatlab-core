package hostd

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

	"github.com/floatlab/floatlab-core/pkg/docker"
	"github.com/floatlab/floatlab-core/pkg/ipc"
	"go.uber.org/zap"
)

// Dispatcher registers all IPC command handlers on the IPC server.
// Each handler is a thin wrapper that shells out or calls a system API.
type Dispatcher struct {
	srv    *ipc.Server
	log    *zap.Logger
	docker *docker.Client // nil if Docker daemon is unavailable at startup
}

func newDispatcher(srv *ipc.Server, dc *docker.Client, log *zap.Logger) *Dispatcher {
	return &Dispatcher{srv: srv, log: log, docker: dc}
}

func (d *Dispatcher) register() {
	d.srv.Handle("compose.up", d.composeUp)
	d.srv.Handle("compose.down", d.composeDown)
	d.srv.Handle("compose.pull", d.composePull)
	d.srv.Handle("fs.dataset.create", d.datasetCreate)
	d.srv.Handle("fs.dataset.destroy", d.datasetDestroy)
	d.srv.Handle("fs.snapshot.create", d.snapshotCreate)
	d.srv.Handle("fs.snapshot.destroy", d.snapshotDestroy)
	d.srv.Handle("fs.repl.send", d.replSend)
	d.srv.Handle("fs.repl.recv", d.replRecv)
	d.srv.Handle("fs.repl.status", d.replStatus)
	d.srv.Handle("net.addr.add", d.netAddrAdd)
	d.srv.Handle("net.addr.del", d.netAddrDel)
	d.srv.Handle("net.route.add", d.netRouteAdd)
	d.srv.Handle("net.route.del", d.netRouteDel)
	d.srv.Handle("sys.info", d.sysInfo)
	d.srv.Handle("sys.docker.events", d.dockerEvents)
	d.srv.Handle("docker.list", d.dockerList)
}

func (d *Dispatcher) composeUp(ctx context.Context, raw json.RawMessage) (any, error) {
	var p ipc.ComposeUpPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("compose.up: parse payload: %w", err)
	}
	composePath := p.DatasetPath + "/docker-compose.yml"
	if err := writeFile(composePath, []byte(p.ComposeFile), 0644); err != nil {
		return nil, fmt.Errorf("compose.up: write compose file: %w", err)
	}
	out, err := runShell(ctx, "docker", "compose", "-f", composePath, "up", "-d", "--remove-orphans")
	if err != nil {
		return nil, fmt.Errorf("compose.up: %w: %s", err, out)
	}
	d.log.Info("compose.up", zap.String("stack", p.StackID))
	return map[string]string{"status": "up"}, nil
}

func (d *Dispatcher) composeDown(ctx context.Context, raw json.RawMessage) (any, error) {
	var p ipc.ComposeDownPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("compose.down: parse payload: %w", err)
	}
	args := []string{"compose", "down"}
	if p.RemoveVolumes {
		args = append(args, "-v")
	}
	_, err := runShell(ctx, "docker", args...)
	return map[string]string{"status": "down"}, err
}

func (d *Dispatcher) composePull(ctx context.Context, raw json.RawMessage) (any, error) {
	var p ipc.ComposePullPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	args := []string{"compose", "pull"}
	args = append(args, p.Services...)
	_, err := runShell(ctx, "docker", args...)
	return map[string]string{"status": "pulled"}, err
}

func (d *Dispatcher) datasetCreate(ctx context.Context, raw json.RawMessage) (any, error) {
	var p ipc.DatasetCreatePayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	args := []string{"create"}
	if p.BlockSize != "" {
		args = append(args, "-o", "recordsize="+p.BlockSize)
	}
	if p.Compression != "" {
		args = append(args, "-o", "compression="+p.Compression)
	}
	if p.Quota != "" {
		args = append(args, "-o", "quota="+p.Quota)
	}
	args = append(args, p.Dataset)
	_, err := runShell(ctx, "zfs", args...)
	return map[string]string{"dataset": p.Dataset}, err
}

func (d *Dispatcher) datasetDestroy(ctx context.Context, raw json.RawMessage) (any, error) {
	var p ipc.DatasetDestroyPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	args := []string{"destroy"}
	if p.Recursive {
		args = append(args, "-r")
	}
	args = append(args, p.Dataset)
	_, err := runShell(ctx, "zfs", args...)
	return map[string]string{"dataset": p.Dataset}, err
}

func (d *Dispatcher) snapshotCreate(ctx context.Context, raw json.RawMessage) (any, error) {
	var p ipc.SnapshotCreatePayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	_, err := runShell(ctx, "zfs", "snapshot", p.Dataset+"@"+p.Name)
	return map[string]string{"snapshot": p.Dataset + "@" + p.Name}, err
}

func (d *Dispatcher) snapshotDestroy(ctx context.Context, raw json.RawMessage) (any, error) {
	var p ipc.SnapshotDestroyPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	_, err := runShell(ctx, "zfs", "destroy", p.Dataset+"@"+p.Name)
	return map[string]string{"snapshot": p.Dataset + "@" + p.Name}, err
}

func (d *Dispatcher) replSend(ctx context.Context, raw json.RawMessage) (any, error) {
	var p ipc.ReplSendPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	// Sprint 2: full implementation with zfs send | ssh zfs recv pipeline.
	d.log.Info("repl.send stub", zap.String("job", p.JobID))
	return map[string]string{"job_id": p.JobID, "status": "queued"}, nil
}

func (d *Dispatcher) replRecv(ctx context.Context, raw json.RawMessage) (any, error) {
	var p ipc.ReplRecvPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	d.log.Info("repl.recv stub", zap.String("job", p.JobID))
	return map[string]string{"job_id": p.JobID, "status": "receiving"}, nil
}

func (d *Dispatcher) replStatus(ctx context.Context, raw json.RawMessage) (any, error) {
	var p ipc.ReplStatusPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	return map[string]string{"job_id": p.JobID, "status": "idle"}, nil
}

func (d *Dispatcher) netAddrAdd(ctx context.Context, raw json.RawMessage) (any, error) {
	var p ipc.NetAddrPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	_, err := runShell(ctx, "ip", "addr", "add", p.Address, "dev", p.Interface)
	return map[string]string{"address": p.Address}, err
}

func (d *Dispatcher) netAddrDel(ctx context.Context, raw json.RawMessage) (any, error) {
	var p ipc.NetAddrPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	_, err := runShell(ctx, "ip", "addr", "del", p.Address, "dev", p.Interface)
	return map[string]string{"address": p.Address}, err
}

func (d *Dispatcher) netRouteAdd(ctx context.Context, raw json.RawMessage) (any, error) {
	var p ipc.NetRoutePayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	_, err := runShell(ctx, "ip", "-6", "route", "add", p.Prefix, "via", p.Nexthop)
	return map[string]string{"prefix": p.Prefix}, err
}

func (d *Dispatcher) netRouteDel(ctx context.Context, raw json.RawMessage) (any, error) {
	var p ipc.NetRoutePayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	_, err := runShell(ctx, "ip", "-6", "route", "del", p.Prefix)
	return map[string]string{"prefix": p.Prefix}, err
}

func (d *Dispatcher) dockerList(ctx context.Context, raw json.RawMessage) (any, error) {
	var p ipc.DockerListPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	if d.docker == nil {
		return nil, fmt.Errorf("docker client not available")
	}
	summaries, err := d.docker.ListByStack(ctx, p.StackID)
	if err != nil {
		return nil, fmt.Errorf("docker.list: %w", err)
	}
	result := ipc.DockerListResult{Containers: make([]ipc.ContainerInfo, 0, len(summaries))}
	for _, s := range summaries {
		result.Containers = append(result.Containers, ipc.ContainerInfo{
			ID:      s.ID,
			Name:    s.Name,
			Image:   s.Image,
			State:   s.State,
			Health:  s.Health,
			Service: s.Service,
			StackID: s.StackID,
		})
	}
	return result, nil
}

func (d *Dispatcher) sysInfo(ctx context.Context, _ json.RawMessage) (any, error) {
	h, _ := runShell(ctx, "hostname")
	k, _ := runShell(ctx, "uname", "-r")
	pools, _ := runShell(ctx, "zpool", "list", "-H", "-o", "name")
	dv, _ := runShell(ctx, "docker", "version", "--format", "{{.Server.Version}}")
	return ipc.SysInfoResult{
		Hostname:      strings.TrimSpace(h),
		KernelVersion: strings.TrimSpace(k),
		ZFSPools:      strings.Fields(pools),
		DockerVersion: strings.TrimSpace(dv),
	}, nil
}

func (d *Dispatcher) dockerEvents(ctx context.Context, raw json.RawMessage) (any, error) {
	var p ipc.DockerEventsPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	if !p.Subscribe {
		return map[string]bool{"subscribed": false}, nil
	}
	if d.docker == nil {
		return nil, fmt.Errorf("docker client not available")
	}
	evCh := make(chan ipc.Event, 128)
	go d.docker.StreamEvents(ctx, evCh, d.log)
	go func() {
		for ev := range evCh {
			d.srv.Emit(ev.Name, json.RawMessage(ev.Payload))
		}
	}()
	d.log.Info("docker event stream started")
	return map[string]bool{"subscribed": true}, nil
}

func runShell(ctx context.Context, name string, args ...string) (string, error) {
	var out strings.Builder
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	return out.String(), err
}

func writeFile(path string, data []byte, perm uint32) error {
	f, err := openFile(path, perm)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(data)
	return err
}
