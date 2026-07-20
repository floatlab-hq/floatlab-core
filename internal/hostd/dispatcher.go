package hostd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/floatlab/floatlab-core/pkg/docker"
	"github.com/floatlab/floatlab-core/pkg/ipc"
	"github.com/floatlab/floatlab-core/pkg/store"
	"go.uber.org/zap"
)

// Dispatcher registers all IPC command handlers on the IPC server.
// Each handler is a thin wrapper that shells out or calls a system API.
type Dispatcher struct {
	srv       *ipc.Server
	log       *zap.Logger
	docker    *docker.Client // nil if Docker daemon is unavailable at startup
	zfs       store.ZFSStore
	mu        sync.Mutex
	terminals map[string]*docker.Terminal
}

func newDispatcher(srv *ipc.Server, dc *docker.Client, log *zap.Logger) *Dispatcher {
	return &Dispatcher{srv: srv, log: log, docker: dc, zfs: store.New(), terminals: make(map[string]*docker.Terminal)}
}

func (d *Dispatcher) register() {
	d.srv.Handle("compose.up", d.composeUp)
	d.srv.Handle("compose.down", d.composeDown)
	d.srv.Handle("compose.pull", d.composePull)
	d.srv.Handle("compose.source.write", d.composeSourceWrite)
	d.srv.Handle("fs.dataset.create", d.datasetCreate)
	d.srv.Handle("fs.dataset.destroy", d.datasetDestroy)
	d.srv.Handle("fs.dataset.set", d.datasetSet)
	d.srv.Handle("fs.dataset.clone", d.datasetClone)
	d.srv.Handle("fs.dataset.rename", d.datasetRename)
	d.srv.Handle("fs.dataset.promote", d.datasetPromote)
	d.srv.Handle("fs.snapshot.create", d.snapshotCreate)
	d.srv.Handle("fs.snapshot.destroy", d.snapshotDestroy)
	d.srv.Handle("fs.repl.send", d.replSend)
	d.srv.Handle("fs.repl.recv", d.replRecv)
	d.srv.Handle("fs.repl.status", d.replStatus)
	d.srv.Handle("net.addr.add", d.netAddrAdd)
	d.srv.Handle("net.addr.del", d.netAddrDel)
	d.srv.Handle("net.route.add", d.netRouteAdd)
	d.srv.Handle("net.route.del", d.netRouteDel)
	d.srv.Handle("net.veth.ensure", d.vethEnsure)
	d.srv.Handle("net.veth.delete", d.vethDelete)
	d.srv.Handle("sys.info", d.sysInfo)
	d.srv.Handle("sys.docker.events", d.dockerEvents)
	d.srv.Handle("docker.list", d.dockerList)
	d.srv.Handle("docker.exec.open", d.terminalOpen)
	d.srv.Handle("docker.exec.write", d.terminalWrite)
	d.srv.Handle("docker.exec.resize", d.terminalResize)
	d.srv.Handle("docker.exec.close", d.terminalClose)
	d.srv.Handle("fs.pool.list", d.poolList)
	d.srv.Handle("fs.pool.health", d.poolHealth)
	d.srv.Handle("fs.snapshot.list", d.snapshotList)
	d.srv.Handle("fs.dataset.list", d.datasetList)
}

func (d *Dispatcher) terminalOpen(ctx context.Context, raw json.RawMessage) (any, error) {
	if d.docker == nil {
		return nil, fmt.Errorf("docker client not available")
	}
	var p ipc.TerminalOpenPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	terminal, err := d.docker.OpenTerminal(ctx, p.StackID, p.ContainerID, p.Command, p.Rows, p.Cols)
	if err != nil {
		return nil, fmt.Errorf("docker.exec.open: %w", err)
	}
	d.mu.Lock()
	d.terminals[terminal.ID] = terminal
	d.mu.Unlock()
	go d.streamTerminal(terminal)
	return ipc.TerminalSessionPayload{SessionID: terminal.ID}, nil
}

func (d *Dispatcher) streamTerminal(terminal *docker.Terminal) {
	buffer := make([]byte, 32*1024)
	for {
		n, err := terminal.Read(buffer)
		if n > 0 {
			d.srv.Emit("docker.exec.output", ipc.TerminalOutputEvent{SessionID: terminal.ID, Data: append([]byte(nil), buffer[:n]...)})
		}
		if err != nil {
			event := ipc.TerminalOutputEvent{SessionID: terminal.ID, Closed: true}
			if !errors.Is(err, io.EOF) {
				event.Error = err.Error()
			}
			d.srv.Emit("docker.exec.output", event)
			d.mu.Lock()
			delete(d.terminals, terminal.ID)
			d.mu.Unlock()
			_ = terminal.Close()
			return
		}
	}
}

func (d *Dispatcher) terminalWrite(_ context.Context, raw json.RawMessage) (any, error) {
	var p ipc.TerminalWritePayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	t := d.getTerminal(p.SessionID)
	if t == nil {
		return nil, fmt.Errorf("terminal session not found")
	}
	_, err := t.Write(p.Data)
	return map[string]bool{"written": err == nil}, err
}

func (d *Dispatcher) terminalResize(ctx context.Context, raw json.RawMessage) (any, error) {
	var p ipc.TerminalResizePayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	t := d.getTerminal(p.SessionID)
	if t == nil {
		return nil, fmt.Errorf("terminal session not found")
	}
	err := t.Resize(ctx, p.Rows, p.Cols)
	return map[string]bool{"resized": err == nil}, err
}

func (d *Dispatcher) terminalClose(_ context.Context, raw json.RawMessage) (any, error) {
	var p ipc.TerminalSessionPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	d.mu.Lock()
	t := d.terminals[p.SessionID]
	delete(d.terminals, p.SessionID)
	d.mu.Unlock()
	if t != nil {
		_ = t.Close()
	}
	return map[string]bool{"closed": true}, nil
}

func (d *Dispatcher) getTerminal(id string) *docker.Terminal {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.terminals[id]
}

func (d *Dispatcher) composeUp(ctx context.Context, raw json.RawMessage) (any, error) {
	var p ipc.ComposeUpPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("compose.up: parse payload: %w", err)
	}
	_, composePath, err := composeArgs(p.StackID, p.DatasetPath)
	if err != nil {
		return nil, fmt.Errorf("compose.up: %w", err)
	}
	runtimePath, cleanup, err := temporaryCompose(composePath, p.ComposeFile)
	if err != nil {
		return nil, fmt.Errorf("compose.up: temporary config: %w", err)
	}
	defer cleanup()
	if d.docker == nil {
		return nil, fmt.Errorf("docker client not available")
	}
	if err := d.docker.ComposeUp(ctx, p.StackID, runtimePath); err != nil {
		return nil, fmt.Errorf("compose.up: %w", err)
	}
	d.log.Info("compose.up", zap.String("stack", p.StackID))
	return map[string]string{"status": "up"}, nil
}

func (d *Dispatcher) composeDown(ctx context.Context, raw json.RawMessage) (any, error) {
	var p ipc.ComposeDownPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("compose.down: parse payload: %w", err)
	}
	_, composePath, err := composeArgs(p.StackID, p.DatasetPath)
	if err != nil {
		return nil, fmt.Errorf("compose.down: %w", err)
	}
	if d.docker == nil {
		return nil, fmt.Errorf("docker client not available")
	}
	err = d.docker.ComposeDown(ctx, p.StackID, composePath, p.RemoveVolumes)
	return map[string]string{"status": "down"}, err
}

func (d *Dispatcher) composePull(ctx context.Context, raw json.RawMessage) (any, error) {
	var p ipc.ComposePullPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	_, composePath, err := composeArgs(p.StackID, p.DatasetPath)
	if err != nil {
		return nil, fmt.Errorf("compose.pull: %w", err)
	}
	pullPath, cleanup, err := temporaryCompose(composePath, p.ComposeFile)
	if err != nil {
		return nil, fmt.Errorf("compose.pull: temporary config: %w", err)
	}
	defer cleanup()
	if d.docker == nil {
		return nil, fmt.Errorf("docker client not available")
	}
	err = d.docker.ComposePull(ctx, p.StackID, pullPath, p.Services)
	return map[string]string{"status": "pulled"}, err
}

func (d *Dispatcher) composeSourceWrite(_ context.Context, raw json.RawMessage) (any, error) {
	var p ipc.ComposeSourcePayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	_, composePath, err := composeArgs(p.StackID, p.DatasetPath)
	if err != nil {
		return nil, err
	}
	return map[string]string{"path": composePath}, writeFile(composePath, []byte(p.ComposeFile), 0644)
}

func temporaryCompose(canonicalPath, content string) (string, func(), error) {
	file, err := os.CreateTemp(filepath.Dir(canonicalPath), ".floatlab-runtime-*.yaml")
	if err != nil {
		return "", func() {}, err
	}
	path := file.Name()
	cleanup := func() { _ = os.Remove(path) }
	if _, err := file.WriteString(content); err != nil {
		file.Close()
		cleanup()
		return "", func() {}, err
	}
	if err := file.Close(); err != nil {
		cleanup()
		return "", func() {}, err
	}
	return path, cleanup, nil
}

func (d *Dispatcher) datasetCreate(ctx context.Context, raw json.RawMessage) (any, error) {
	var p ipc.DatasetCreatePayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	if !validDataset(p.Dataset) {
		return nil, fmt.Errorf("invalid dataset")
	}
	if (p.BlockSize != "" && !zfsRecordSize.MatchString(strings.ToUpper(p.BlockSize))) || (p.Compression != "" && !zfsCompression.MatchString(strings.ToLower(p.Compression))) || (p.Quota != "" && p.Quota != "none" && !zfsSize.MatchString(p.Quota)) {
		return nil, fmt.Errorf("invalid ZFS properties")
	}
	if _, err := runShell(ctx, "zfs", "list", "-H", "-o", "name", p.Dataset); err == nil {
		return map[string]string{"dataset": p.Dataset}, nil
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
	if !validDataset(p.Dataset) {
		return nil, fmt.Errorf("invalid dataset")
	}
	if _, err := runShell(ctx, "zfs", "list", "-H", "-o", "name", p.Dataset); err != nil {
		return map[string]string{"dataset": p.Dataset}, nil
	}
	args := []string{"destroy"}
	if p.Recursive {
		args = append(args, "-r")
	}
	args = append(args, p.Dataset)
	_, err := runShell(ctx, "zfs", args...)
	return map[string]string{"dataset": p.Dataset}, err
}

func (d *Dispatcher) datasetSet(ctx context.Context, raw json.RawMessage) (any, error) {
	var p ipc.DatasetSetPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	if !validDataset(p.Dataset) {
		return nil, fmt.Errorf("invalid dataset")
	}
	allowed := map[string]bool{"recordsize": true, "compression": true, "quota": true, "mountpoint": true, "readonly": true}
	for property, value := range p.Properties {
		if !allowed[property] || value == "" || strings.ContainsAny(value, "\x00\n\r") {
			return nil, fmt.Errorf("invalid ZFS property")
		}
		if _, err := runShell(ctx, "zfs", "set", property+"="+value, p.Dataset); err != nil {
			return nil, err
		}
	}
	return map[string]string{"dataset": p.Dataset}, nil
}

func (d *Dispatcher) datasetClone(ctx context.Context, raw json.RawMessage) (any, error) {
	var p ipc.DatasetClonePayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	if !validSnapshot(p.Snapshot) || !validDataset(p.Target) {
		return nil, fmt.Errorf("invalid clone target")
	}
	if _, err := runShell(ctx, "zfs", "list", "-H", "-o", "name", p.Target); err == nil {
		return map[string]string{"dataset": p.Target}, nil
	}
	_, err := runShell(ctx, "zfs", "clone", p.Snapshot, p.Target)
	return map[string]string{"dataset": p.Target}, err
}

func (d *Dispatcher) datasetRename(ctx context.Context, raw json.RawMessage) (any, error) {
	var p ipc.DatasetRenamePayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	if !validDataset(p.Source) || !validDataset(p.Target) {
		return nil, fmt.Errorf("invalid rename target")
	}
	if _, err := runShell(ctx, "zfs", "list", "-H", "-o", "name", p.Source); err != nil {
		if _, targetErr := runShell(ctx, "zfs", "list", "-H", "-o", "name", p.Target); targetErr == nil {
			return map[string]string{"dataset": p.Target}, nil
		}
	}
	args := []string{"rename"}
	if p.Recursive {
		args = append(args, "-r")
	}
	_, err := runShell(ctx, "zfs", append(args, p.Source, p.Target)...)
	return map[string]string{"dataset": p.Target}, err
}

func (d *Dispatcher) datasetPromote(ctx context.Context, raw json.RawMessage) (any, error) {
	var p ipc.DatasetPromotePayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	if !validDataset(p.Dataset) {
		return nil, fmt.Errorf("invalid dataset")
	}
	if origin, err := runShell(ctx, "zfs", "get", "-H", "-o", "value", "origin", p.Dataset); err == nil && strings.TrimSpace(origin) == "-" {
		return map[string]string{"dataset": p.Dataset}, nil
	}
	_, err := runShell(ctx, "zfs", "promote", p.Dataset)
	return map[string]string{"dataset": p.Dataset}, err
}

func (d *Dispatcher) snapshotCreate(ctx context.Context, raw json.RawMessage) (any, error) {
	var p ipc.SnapshotCreatePayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	target := p.Dataset + "@" + p.Name
	if !validSnapshot(target) {
		return nil, fmt.Errorf("invalid snapshot")
	}
	if _, err := runShell(ctx, "zfs", "list", "-H", "-o", "name", "-t", "snapshot", target); err == nil {
		return map[string]string{"snapshot": target}, nil
	}
	args := []string{"snapshot"}
	if p.Recursive {
		args = append(args, "-r")
	}
	args = append(args, target)
	_, err := runShell(ctx, "zfs", args...)
	return map[string]string{"snapshot": target}, err
}

func (d *Dispatcher) snapshotDestroy(ctx context.Context, raw json.RawMessage) (any, error) {
	var p ipc.SnapshotDestroyPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	target := p.Dataset + "@" + p.Name
	if !validSnapshot(target) {
		return nil, fmt.Errorf("invalid snapshot")
	}
	if _, err := runShell(ctx, "zfs", "list", "-H", "-o", "name", "-t", "snapshot", target); err != nil {
		return map[string]string{"snapshot": target}, nil
	}
	_, err := runShell(ctx, "zfs", "destroy", target)
	return map[string]string{"snapshot": target}, err
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

func (d *Dispatcher) vethEnsure(ctx context.Context, raw json.RawMessage) (any, error) {
	var p ipc.VethPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	if !validInterface(p.HostName) || !validInterface(p.PeerName) || !composeProject.MatchString(p.StackID) || p.Bridge != "floatlab-lan" {
		return nil, fmt.Errorf("invalid veth parameters")
	}
	alias := "floatlab:" + p.StackID
	if output, err := runShell(ctx, "ip", "-d", "link", "show", "dev", p.HostName); err == nil {
		if !strings.Contains(output, "alias "+alias) {
			return nil, fmt.Errorf("interface %s is not owned by stack", p.HostName)
		}
	} else {
		if _, err := runShell(ctx, "ip", "link", "add", p.HostName, "type", "veth", "peer", "name", p.PeerName); err != nil {
			return nil, err
		}
		if _, err := runShell(ctx, "ip", "link", "set", "dev", p.HostName, "alias", alias); err != nil {
			_, _ = runShell(ctx, "ip", "link", "delete", p.HostName)
			return nil, err
		}
	}
	if p.Address != "" {
		if _, _, err := net.ParseCIDR(p.Address); err != nil {
			return nil, fmt.Errorf("invalid veth address")
		}
		if _, err := runShell(ctx, "ip", "addr", "replace", p.Address, "dev", p.HostName); err != nil {
			return nil, err
		}
	}
	commands := [][]string{{"link", "set", p.HostName, "up"}, {"link", "set", p.PeerName, "master", p.Bridge}, {"link", "set", p.PeerName, "up"}}
	for _, args := range commands {
		if _, err := runShell(ctx, "ip", args...); err != nil {
			return nil, err
		}
	}
	return map[string]string{"host": p.HostName, "peer": p.PeerName}, nil
}

func (d *Dispatcher) vethDelete(ctx context.Context, raw json.RawMessage) (any, error) {
	var p ipc.VethPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	if !validInterface(p.HostName) || !composeProject.MatchString(p.StackID) {
		return nil, fmt.Errorf("invalid veth parameters")
	}
	output, err := runShell(ctx, "ip", "-d", "link", "show", "dev", p.HostName)
	if err != nil {
		return map[string]bool{"deleted": false}, nil
	}
	if !strings.Contains(output, "alias floatlab:"+p.StackID) {
		return nil, fmt.Errorf("interface %s is not owned by stack", p.HostName)
	}
	_, err = runShell(ctx, "ip", "link", "delete", p.HostName)
	return map[string]bool{"deleted": err == nil}, err
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
			ID:       s.ID,
			Name:     s.Name,
			Image:    s.Image,
			State:    s.State,
			Health:   s.Health,
			Service:  s.Service,
			StackID:  s.StackID,
			ExitCode: s.ExitCode,
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

func (d *Dispatcher) poolList(ctx context.Context, _ json.RawMessage) (any, error) {
	pools, err := d.zfs.PoolList(ctx)
	if err != nil {
		return nil, fmt.Errorf("fs.pool.list: %w", err)
	}
	result := ipc.PoolListResult{Pools: make([]ipc.PoolSummaryResult, 0, len(pools))}
	for _, p := range pools {
		result.Pools = append(result.Pools, ipc.PoolSummaryResult{
			Name:      p.Name,
			Health:    p.Health,
			Used:      p.Used,
			Available: p.Available,
		})
	}
	return result, nil
}

func (d *Dispatcher) poolHealth(ctx context.Context, raw json.RawMessage) (any, error) {
	var p ipc.PoolHealthPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	status, err := d.zfs.PoolHealth(ctx, p.Pool)
	if err != nil {
		return nil, fmt.Errorf("fs.pool.health: %w", err)
	}
	result := ipc.PoolHealthResult{
		Name:   status.Name,
		Health: status.Health,
		Errors: status.Errors,
		VDevs:  make([]ipc.PoolVDevInfo, 0, len(status.VDevs)),
	}
	for _, v := range status.VDevs {
		result.VDevs = append(result.VDevs, ipc.PoolVDevInfo{Name: v.Name, State: v.State})
	}
	return result, nil
}

func (d *Dispatcher) snapshotList(ctx context.Context, raw json.RawMessage) (any, error) {
	var p ipc.SnapshotListPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	snaps, err := d.zfs.SnapshotList(ctx, p.Dataset)
	if err != nil {
		return nil, fmt.Errorf("fs.snapshot.list: %w", err)
	}
	result := ipc.SnapshotListResult{Snapshots: make([]ipc.SnapshotInfoResult, 0, len(snaps))}
	for _, s := range snaps {
		result.Snapshots = append(result.Snapshots, ipc.SnapshotInfoResult{
			Name:      s.Name,
			Dataset:   s.Dataset,
			Used:      s.Used,
			CreatedAt: s.CreatedAt,
		})
	}
	return result, nil
}

func (d *Dispatcher) datasetList(ctx context.Context, raw json.RawMessage) (any, error) {
	var p ipc.DatasetListPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	datasets, err := d.zfs.DatasetList(ctx, p.Parent)
	if err != nil {
		return nil, fmt.Errorf("fs.dataset.list: %w", err)
	}
	result := ipc.DatasetListResult{Datasets: make([]ipc.DatasetInfoResult, 0, len(datasets))}
	for _, ds := range datasets {
		result.Datasets = append(result.Datasets, ipc.DatasetInfoResult{
			Name:       ds.Name,
			Used:       ds.Used,
			Available:  ds.Available,
			Quota:      ds.Quota,
			Mountpoint: ds.Mountpoint,
		})
	}
	return result, nil
}

func runShell(ctx context.Context, name string, args ...string) (string, error) {
	var out strings.Builder
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	return out.String(), err
}

var (
	composeProject = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)
	datasetPart    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]*$`)
	interfaceName  = regexp.MustCompile(`^[a-zA-Z0-9_.-]{1,15}$`)
	zfsRecordSize  = regexp.MustCompile(`^(4K|8K|16K|32K|64K|128K|256K|512K|1M)$`)
	zfsCompression = regexp.MustCompile(`^(none|lz4|gzip|zstd)$`)
	zfsSize        = regexp.MustCompile(`^[1-9][0-9]*([KMGTPE]i?B?|[kmgtpe])?$`)
)

func validInterface(value string) bool { return interfaceName.MatchString(value) }

func validDataset(value string) bool {
	if value == "" {
		return false
	}
	for _, part := range strings.Split(value, "/") {
		if !datasetPart.MatchString(part) {
			return false
		}
	}
	return true
}

func validSnapshot(value string) bool {
	parts := strings.Split(value, "@")
	return len(parts) == 2 && validDataset(parts[0]) && datasetPart.MatchString(parts[1])
}

func composeArgs(stackID, dataset string, action ...string) ([]string, string, error) {
	if !composeProject.MatchString(stackID) {
		return nil, "", fmt.Errorf("invalid stack ID")
	}
	parts := strings.Split(dataset, "/")
	for _, part := range parts {
		if !datasetPart.MatchString(part) {
			return nil, "", fmt.Errorf("invalid dataset path")
		}
	}
	path := filepath.Join(append([]string{"/"}, append(parts, "docker-compose.yml")...)...)
	args := []string{"compose", "-p", stackID, "-f", path}
	return append(args, action...), path, nil
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
