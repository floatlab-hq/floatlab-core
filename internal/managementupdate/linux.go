package managementupdate

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/floatlab/floatlab-core/internal/update"
	"github.com/floatlab/floatlab-core/pkg/release"
	"gopkg.in/yaml.v3"
)

const managementDataset = "floatlab/system/management"
const composePath = "/floatlab/system/docker-compose.yml"
const officialRepository = "floatlab-hq/floatlab-core"
const activationDir = "/run/floatlab/activation"

var digest = regexp.MustCompile(`^[a-zA-Z0-9./:_-]+@sha256:[a-f0-9]{64}$`)

type Manifest struct {
	Version       string            `json:"version"`
	Architecture  string            `json:"architecture"`
	RequiredHost  release.HostRange `json:"required_host"`
	Images        map[string]string `json:"images"`
	RQLiteSources []string          `json:"rqlite_sources"`
	RQLiteVersion string            `json:"rqlite_version"`
}
type Linux struct {
	Dir     string
	Run     func(context.Context, string, ...string) (string, error)
	Quiesce func(context.Context) error
}

func (l *Linux) run(ctx context.Context, name string, args ...string) (string, error) {
	if l.Run != nil {
		return l.Run(ctx, name, args...)
	}
	b, e := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if e != nil {
		return string(b), fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), e, b)
	}
	return strings.TrimSpace(string(b)), nil
}
func validRelease(tag string) bool {
	if !strings.HasPrefix(tag, "manage-v") {
		return false
	}
	_, e := release.Parse(strings.TrimPrefix(tag, "manage-v"))
	return e == nil
}
func (m Manifest) Validate(tag string) error {
	if !validRelease(tag) || m.Version != strings.TrimPrefix(tag, "manage-v") || m.Architecture != "amd64" {
		return fmt.Errorf("invalid release identity or architecture")
	}
	ok, e := m.RequiredHost.Contains(release.Version)
	if e != nil || !ok {
		return fmt.Errorf("release incompatible with host %s", release.Version)
	}
	if len(m.Images) != 4 || len(m.RQLiteSources) == 0 {
		return fmt.Errorf("release must pin four services and tested rqlite source versions")
	}
	for _, service := range []string{"floatlab-control", "rqlite", "victoria-metrics", "victoria-logs"} {
		if !digest.MatchString(m.Images[service]) {
			return fmt.Errorf("unpinned image: %s", service)
		}
	}
	if _, e := release.Parse(m.RQLiteVersion); e != nil {
		return e
	}
	for _, v := range m.RQLiteSources {
		if _, e := release.Parse(v); e != nil {
			return e
		}
	}
	return nil
}
func fetchJSON(ctx context.Context, address string, value any) error {
	u, e := url.Parse(address)
	if e != nil || u.Scheme != "https" {
		return fmt.Errorf("official HTTPS source required")
	}
	req, e := http.NewRequestWithContext(ctx, "GET", address, nil)
	if e != nil {
		return e
	}
	client := &http.Client{Timeout: 45 * time.Second, CheckRedirect: func(r *http.Request, via []*http.Request) error {
		if r.URL.Scheme != "https" || len(via) > 5 {
			return fmt.Errorf("invalid release redirect")
		}
		return nil
	}}
	r, e := client.Do(req)
	if e != nil {
		return e
	}
	defer r.Body.Close()
	if r.StatusCode != 200 {
		return fmt.Errorf("release server returned %s", r.Status)
	}
	return json.NewDecoder(io.LimitReader(r.Body, 2<<20)).Decode(value)
}

type Release struct {
	Tag         string `json:"tag_name"`
	Name        string `json:"name"`
	PublishedAt string `json:"published_at"`
	Draft       bool   `json:"draft"`
	Prerelease  bool   `json:"prerelease"`
}

func Releases(ctx context.Context) ([]Release, error) {
	var rows []Release
	if e := fetchJSON(ctx, "https://api.github.com/repos/"+officialRepository+"/releases?per_page=100", &rows); e != nil {
		return nil, e
	}
	out := []Release{}
	for _, r := range rows {
		if !r.Draft && !r.Prerelease && validRelease(r.Tag) {
			out = append(out, r)
		}
	}
	return out, nil
}
func (l *Linux) Prepare(ctx context.Context, s *Status) error {
	if e := fetchJSON(ctx, "https://github.com/"+officialRepository+"/releases/download/"+s.TargetRelease+"/manage-manifest.json", &s.Manifest); e != nil {
		return e
	}
	if e := s.Manifest.Validate(s.TargetRelease); e != nil {
		return e
	}
	if e := l.capture(ctx, s); e != nil {
		return e
	}
	health, e := l.run(ctx, "zpool", "list", "-H", "-o", "health", "floatlab")
	if e != nil {
		return e
	}
	if strings.TrimSpace(health) != "ONLINE" {
		return fmt.Errorf("pool is not ONLINE")
	}
	available, e := l.run(ctx, "zfs", "get", "-Hp", "-o", "value", "available", managementDataset)
	if e != nil {
		return e
	}
	n, e := strconv.ParseUint(strings.TrimSpace(available), 10, 64)
	if e != nil || n < 2<<30 {
		return fmt.Errorf("at least 2 GiB free pool capacity required")
	}
	version, e := l.run(ctx, "docker", "exec", l.controlID(ctx), "wget", "-qO-", "http://rqlite:4001/status")
	if e != nil {
		return e
	}
	var status struct {
		Build struct {
			Version string `json:"version"`
		} `json:"build"`
		Store struct {
			Nodes []any `json:"nodes"`
		} `json:"store"`
	}
	if e = json.Unmarshal([]byte(version), &status); e != nil {
		return e
	}
	supported := false
	for _, v := range s.Manifest.RQLiteSources {
		if v == status.Build.Version {
			supported = true
		}
	}
	if !supported {
		return fmt.Errorf("rqlite source %q has no tested transition", status.Build.Version)
	}
	nodes, e := l.run(ctx, "docker", "exec", l.controlID(ctx), "wget", "-qO-", "http://rqlite:4001/nodes")
	if e != nil {
		return e
	}
	var peers map[string]any
	if e = json.Unmarshal([]byte(nodes), &peers); e != nil || len(peers) != 1 {
		return fmt.Errorf("management updates require standalone rqlite")
	}
	for _, image := range s.Manifest.Images {
		if _, e = l.run(ctx, "docker", "pull", "--platform", "linux/amd64", image); e != nil {
			return e
		}
		info, e := l.run(ctx, "docker", "image", "inspect", "--format", "{{.Architecture}} {{.Os}}", image)
		if e != nil {
			return e
		}
		if info != "amd64 linux" {
			return fmt.Errorf("image platform mismatch")
		}
	}
	doc, e := composeDocument(s.PreviousCompose)
	if e != nil {
		return e
	}
	for name, service := range services(doc) {
		service["image"] = s.Manifest.Images[name]
		service["restart"] = "no"
		if name == "floatlab-control" {
			setEnvironment(service, "FLOATLAB_ACTIVATION_DIR", activationDir)
			setEnvironment(service, "FLOATLAB_UPDATE_RELEASE", s.TargetRelease)
		}
	}
	s.CandidateCompose, e = yaml.Marshal(doc)
	return e
}
func (l *Linux) controlID(ctx context.Context) string {
	id, _ := l.run(ctx, "docker", "ps", "-q", "--filter", "label=com.docker.compose.project=floatlab-system", "--filter", "label=com.docker.compose.service=floatlab-control")
	return strings.TrimSpace(id)
}
func (l *Linux) capture(ctx context.Context, s *Status) error {
	info, err := l.run(ctx, "docker", "exec", l.controlID(ctx), "/usr/local/bin/floatlab-control", "--release-info")
	if err != nil {
		return fmt.Errorf("management baseline does not support safe updates; install an update-capable baseline first: %w", err)
	}
	if err = validateBaseline([]byte(info)); err != nil {
		return err
	}
	var baseline struct {
		Version string `json:"version"`
	}
	_ = json.Unmarshal([]byte(info), &baseline)
	s.SourceRelease = "manage-v" + baseline.Version
	b, e := os.ReadFile(composePath)
	if e != nil {
		return e
	}
	s.PreviousCompose = b
	doc, e := composeDocument(b)
	if e != nil {
		return e
	}
	if e = l.freezeDefinitions(ctx, s, doc); e != nil {
		return e
	}
	s.PreviousCompose, e = yaml.Marshal(doc)
	if e != nil {
		return e
	}
	if s.SourceRelease == "" {
		s.SourceRelease = "installed"
	}
	s.Datasets, e = l.datasets(ctx)
	if e == nil {
		e = l.verifyMountedDatasets(ctx, s.Datasets)
	}
	if e == nil {
		e = verifyManagementBinds(doc)
	}
	return e
}
func (l *Linux) freezeDefinitions(ctx context.Context, s *Status, doc map[string]any) error {
	s.Policies = map[string]string{}
	for name, svc := range services(doc) {
		image, ok := svc["image"].(string)
		if !ok || image == "" {
			return fmt.Errorf("missing original image")
		}
		containerIDs, err := l.run(ctx, "docker", "ps", "-aq", "--filter", "label=com.docker.compose.project=floatlab-system", "--filter", "label=com.docker.compose.service="+name)
		if err != nil {
			return err
		}
		ids := strings.Fields(containerIDs)
		if len(ids) != 1 {
			return fmt.Errorf("expected exactly one installed container for %s", name)
		}
		imageID, err := l.run(ctx, "docker", "inspect", "--format", "{{.Image}}", ids[0])
		if err != nil {
			return fmt.Errorf("old image unavailable: %w", err)
		}
		if !regexp.MustCompile(`^sha256:[a-f0-9]{64}$`).MatchString(imageID) {
			return fmt.Errorf("invalid installed image identity")
		}
		retained := "floatlab-rollback/" + name + ":" + strings.TrimPrefix(imageID, "sha256:")
		if _, e := l.run(ctx, "docker", "tag", imageID, retained); e != nil {
			return e
		}
		svc["image"] = retained
		rawPolicy, err := l.run(ctx, "docker", "inspect", "--format", "{{json .HostConfig.RestartPolicy}}", ids[0])
		if err != nil {
			return err
		}
		var actualPolicy struct {
			Name              string
			MaximumRetryCount int
		}
		if err = json.Unmarshal([]byte(rawPolicy), &actualPolicy); err != nil {
			return err
		}
		policy := actualPolicy.Name
		switch policy {
		case "no", "always", "unless-stopped":
		case "on-failure":
			if actualPolicy.MaximumRetryCount > 0 {
				policy += ":" + strconv.Itoa(actualPolicy.MaximumRetryCount)
			}
		default:
			return fmt.Errorf("unsupported installed restart policy %q", policy)
		}
		s.Policies[name] = policy
	}
	return nil
}
func validateBaseline(b []byte) error {
	var info struct {
		Version  string `json:"version"`
		Protocol int    `json:"update_protocol"`
	}
	if e := json.Unmarshal(b, &info); e != nil {
		return e
	}
	if info.Protocol != 1 {
		return fmt.Errorf("management baseline does not support safe update protocol 1; install an update-capable baseline first")
	}
	if _, e := release.Parse(info.Version); e != nil {
		return e
	}
	return nil
}
func (l *Linux) datasets(ctx context.Context) ([]Dataset, error) {
	raw, e := l.run(ctx, "zfs", "list", "-H", "-r", "-t", "filesystem", "-o", "name,guid", managementDataset)
	if e != nil {
		return nil, e
	}
	var out []Dataset
	for _, line := range strings.Split(raw, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || !(fields[0] == managementDataset || strings.HasPrefix(fields[0], managementDataset+"/")) {
			return nil, fmt.Errorf("invalid management dataset membership")
		}
		out = append(out, Dataset{fields[0], fields[1]})
	}
	if len(out) < 5 {
		return nil, fmt.Errorf("management hierarchy adoption incomplete")
	}
	return out, nil
}
func composeDocument(b []byte) (map[string]any, error) {
	var doc map[string]any
	if e := yaml.Unmarshal(b, &doc); e != nil {
		return nil, e
	}
	ss, ok := doc["services"].(map[string]any)
	if !ok || len(ss) != 4 {
		return nil, fmt.Errorf("expected exactly four management services")
	}
	for _, name := range []string{"floatlab-control", "rqlite", "victoria-metrics", "victoria-logs"} {
		if _, ok := ss[name].(map[string]any); !ok {
			return nil, fmt.Errorf("missing service %s", name)
		}
	}
	return doc, nil
}
func services(doc map[string]any) map[string]map[string]any {
	out := map[string]map[string]any{}
	for name, v := range doc["services"].(map[string]any) {
		out[name] = v.(map[string]any)
	}
	return out
}
func setEnvironment(service map[string]any, key, value string) {
	env, ok := service["environment"].(map[string]any)
	if !ok {
		env = map[string]any{}
	}
	env[key] = value
	service["environment"] = env
}
func (l *Linux) compose(ctx context.Context, path, project string, args ...string) error {
	_, e := l.run(ctx, "docker", append([]string{"compose", "-p", project, "-f", path}, args...)...)
	return e
}
func (l *Linux) noRestart(b []byte) ([]byte, error) {
	doc, e := composeDocument(b)
	if e != nil {
		return nil, e
	}
	for _, s := range services(doc) {
		s["restart"] = "no"
	}
	return yaml.Marshal(doc)
}
func (l *Linux) Fence(ctx context.Context, s *Status) error {
	if e := os.MkdirAll(activationDir, 0750); e != nil {
		return e
	}
	for _, name := range []string{"controllers-drained", "api-drained"} {
		if e := os.Remove(filepath.Join(activationDir, name)); e != nil && !os.IsNotExist(e) {
			return e
		}
	}
	if e := update.WriteFile(filepath.Join(activationDir, "blocked"), []byte(s.OperationID), 0600); e != nil {
		return e
	}
	// Await cancellation and completion of every controller before stopping database writers.
	if l.controlID(ctx) != "" {
		deadline := time.Now().Add(time.Minute)
		for {
			controllers, controllersErr := os.ReadFile(filepath.Join(activationDir, "controllers-drained"))
			api, apiErr := os.ReadFile(filepath.Join(activationDir, "api-drained"))
			if controllersErr == nil && apiErr == nil && string(controllers) == s.OperationID && string(api) == s.OperationID {
				break
			}
			if time.Now().After(deadline) {
				return fmt.Errorf("management controllers did not drain")
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(200 * time.Millisecond):
			}
		}
	}
	if l.Quiesce != nil {
		if e := l.Quiesce(ctx); e != nil {
			return e
		}
	}
	return l.suppressAndStop(ctx, s)
}
func (l *Linux) suppressAndStop(ctx context.Context, s *Status) error {
	if e := update.WriteFile(filepath.Join(activationDir, "blocked"), []byte(s.OperationID), 0600); e != nil {
		return e
	}
	// The persistent gate is consumed by both systemd and hostd startup.
	if e := update.WriteFile(filepath.Join(l.Dir, "management-blocked"), []byte(s.OperationID), 0600); e != nil {
		return e
	}
	if _, e := l.run(ctx, "systemctl", "mask", "--runtime", "floatlab-core-stack.service"); e != nil {
		return e
	}
	suppressed, e := l.noRestart(s.PreviousCompose)
	if e != nil {
		return e
	}
	if e = update.WriteFile(composePath, suppressed, 0644); e != nil {
		return e
	}
	ids, e := l.run(ctx, "docker", "ps", "-aq", "--filter", "label=com.docker.compose.project=floatlab-system")
	if e != nil {
		return e
	}
	for _, id := range strings.Fields(ids) {
		if _, e = l.run(ctx, "docker", "update", "--restart=no", id); e != nil {
			return e
		}
		policy, e := l.run(ctx, "docker", "inspect", "--format", "{{.HostConfig.RestartPolicy.Name}}", id)
		if e != nil {
			return e
		}
		if policy != "no" {
			return fmt.Errorf("restart suppression not verified")
		}
	}
	if e = l.compose(ctx, composePath, "floatlab-system", "stop", "--timeout", "60"); e != nil {
		return e
	}
	return l.stopped(ctx)
}
func (l *Linux) stopped(ctx context.Context) error {
	ids, e := l.run(ctx, "docker", "ps", "-q", "--filter", "label=com.docker.compose.project=floatlab-system")
	if e != nil {
		return e
	}
	if strings.TrimSpace(ids) != "" {
		return fmt.Errorf("management writers still running")
	}
	return nil
}
func (l *Linux) Snapshot(ctx context.Context, s *Status) error {
	if e := l.stopped(ctx); e != nil {
		return e
	}
	actual, e := l.datasets(ctx)
	if e != nil {
		return e
	}
	s.Datasets = actual
	s.Snapshot = "floatlab-update-" + s.OperationID
	return l.snapshot(ctx, s, s.Snapshot, true)
}
func (l *Linux) snapshot(ctx context.Context, s *Status, name string, hold bool) error {
	if _, e := l.run(ctx, "zfs", "snapshot", "-r", managementDataset+"@"+name); e != nil {
		return e
	}
	if hold {
		for _, ds := range s.Datasets {
			if _, e := l.run(ctx, "zfs", "hold", "floatlab-update", ds.Name+"@"+name); e != nil {
				return e
			}
		}
	}
	return nil
}
func (l *Linux) Install(ctx context.Context, s *Status) error {
	if e := l.verifyMountedDatasets(ctx, s.Datasets); e != nil {
		return e
	}
	if e := update.WriteFile(composePath, s.CandidateCompose, 0644); e != nil {
		return e
	}
	if e := l.compose(ctx, composePath, "floatlab-system", "up", "-d", "--pull", "never", "--wait", "--wait-timeout", "300"); e != nil {
		return e
	}
	return nil
}
func (l *Linux) Healthy(ctx context.Context, s *Status) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	expected := strings.TrimPrefix(s.SourceRelease, "manage-v")
	if s.Phase == "checking" {
		expected = s.Manifest.Version
	}
	var stable time.Time
	for {
		id := l.controlID(ctx)
		_, e := l.run(ctx, "docker", "exec", id, "/usr/local/bin/floatlab-control", "--self-test", "--expected-release="+expected, "--rqlite-url=http://rqlite:4001", "--vlogs-url=http://victoria-logs:9428", "--vmetrics-url=http://victoria-metrics:8428")
		if e == nil {
			e = l.verifyRunningImages(ctx, s)
		}
		if e == nil {
			if stable.IsZero() {
				stable = time.Now()
			}
			if time.Since(stable) >= time.Minute {
				return nil
			}
		} else {
			stable = time.Time{}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("management readiness deadline: %w (last check: %v)", ctx.Err(), e)
		case <-time.After(5 * time.Second):
		}
	}
}

func (l *Linux) verifyRunningImages(ctx context.Context, s *Status) error {
	images := s.Manifest.Images
	if s.Phase != "checking" {
		doc, e := composeDocument(s.PreviousCompose)
		if e != nil {
			return e
		}
		images = map[string]string{}
		for name, service := range services(doc) {
			images[name] = service["image"].(string)
		}
	}
	for name, ref := range images {
		ids, e := l.run(ctx, "docker", "ps", "-q", "--filter", "label=com.docker.compose.project=floatlab-system", "--filter", "label=com.docker.compose.service="+name)
		if e != nil {
			return e
		}
		running := strings.Fields(ids)
		if len(running) != 1 {
			return fmt.Errorf("expected one running %s service", name)
		}
		actual, e := l.run(ctx, "docker", "inspect", "--format", "{{.Image}}", running[0])
		if e != nil {
			return e
		}
		expected, e := l.run(ctx, "docker", "image", "inspect", "--format", "{{.Id}}", ref)
		if e != nil {
			return e
		}
		if actual != expected {
			return fmt.Errorf("running %s image differs from authorized release", name)
		}
	}
	return nil
}
func (l *Linux) Restore(ctx context.Context, s *Status) error {
	// A broken candidate cannot acknowledge a drain. Stop every candidate writer before any restoration.
	if e := l.suppressAndStop(ctx, s); e != nil {
		return e
	}
	if s.ProductionChanged {
		if s.Snapshot == "" {
			return fmt.Errorf("rollback snapshot identity missing")
		}
		actual, e := l.datasets(ctx)
		if e != nil {
			return e
		}
		if len(actual) != len(s.Datasets) {
			return fmt.Errorf("dataset membership changed; manual recovery required")
		}
		for i, ds := range actual {
			if ds != s.Datasets[i] {
				return fmt.Errorf("dataset identity changed; manual recovery required")
			}
		}
		if e = l.restoreDatasets(ctx, s); e != nil {
			return e
		}
	}
	b, e := l.noRestart(s.PreviousCompose)
	if e != nil {
		return e
	}
	if e = update.WriteFile(composePath, b, 0644); e != nil {
		return e
	}
	if e = l.verifyMountedDatasets(ctx, s.Datasets); e != nil {
		return e
	}
	if e = l.compose(ctx, composePath, "floatlab-system", "up", "-d", "--pull", "never", "--wait", "--wait-timeout", "300"); e != nil {
		stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
		defer cancel()
		_ = l.compose(stopCtx, composePath, "floatlab-system", "stop", "--timeout", "60")
		return e
	}
	return nil
}

func (l *Linux) restoreDatasets(ctx context.Context, s *Status) error {
	if e := l.verifyDatasetProperties(ctx, s.Datasets); e != nil {
		return e
	}
	// Removing stopped containers releases bind mounts; unmounting drops stale
	// mmap pages that ZFS rollback can otherwise retain (OpenZFS issue #10931).
	if e := l.compose(ctx, composePath, "floatlab-system", "rm", "-f"); e != nil {
		return e
	}
	for _, ds := range s.Datasets {
		guid, e := l.run(ctx, "zfs", "get", "-H", "-o", "value", "guid", ds.Name+"@"+s.Snapshot)
		if e != nil || strings.TrimSpace(guid) == "" {
			return fmt.Errorf("rollback snapshot unavailable: %s", ds.Name)
		}
	}
	for i := len(s.Datasets) - 1; i >= 0; i-- {
		ds := s.Datasets[i]
		mounted, e := l.run(ctx, "zfs", "get", "-H", "-o", "value", "mounted", ds.Name)
		if e != nil {
			return e
		}
		switch mounted {
		case "yes":
			if e = l.verifyMountedDataset(ctx, ds); e != nil {
				return e
			}
			if _, e = l.run(ctx, "zfs", "unmount", ds.Name); e != nil {
				return e
			}
		case "no":
		default:
			return fmt.Errorf("unknown mount state for %s", ds.Name)
		}
	}
	for _, ds := range s.Datasets {
		mounted, e := l.run(ctx, "zfs", "get", "-H", "-o", "value", "mounted", ds.Name)
		if e != nil {
			return e
		}
		if mounted != "no" {
			return fmt.Errorf("management dataset %s remains mounted before rollback", ds.Name)
		}
	}
	// Plain rollback refuses unrelated newer snapshots and clones; never use -r/-R.
	for i := len(s.Datasets) - 1; i >= 0; i-- {
		if _, e := l.run(ctx, "zfs", "rollback", s.Datasets[i].Name+"@"+s.Snapshot); e != nil {
			return e
		}
	}
	for _, ds := range s.Datasets {
		mounted, e := l.run(ctx, "zfs", "get", "-H", "-o", "value", "mounted", ds.Name)
		if e != nil {
			return e
		}
		if mounted == "no" {
			if _, e = l.run(ctx, "zfs", "mount", ds.Name); e != nil {
				return e
			}
		} else if mounted != "yes" {
			return fmt.Errorf("unknown mount state for %s", ds.Name)
		}
	}
	return nil
}

func (l *Linux) verifyDatasetProperties(ctx context.Context, datasets []Dataset) error {
	for _, ds := range datasets {
		raw, e := l.run(ctx, "zfs", "get", "-H", "-o", "property,value", "mountpoint,canmount", ds.Name)
		if e != nil {
			return e
		}
		props := map[string]string{}
		for _, line := range strings.Split(raw, "\n") {
			fields := strings.Fields(line)
			if len(fields) != 2 {
				return fmt.Errorf("invalid management mount properties")
			}
			props[fields[0]] = fields[1]
		}
		if props["mountpoint"] != "/"+ds.Name || props["canmount"] != "on" {
			return fmt.Errorf("management dataset %s must use mountpoint /%s and canmount=on", ds.Name, ds.Name)
		}
	}
	return nil
}
func (l *Linux) verifyMountedDatasets(ctx context.Context, datasets []Dataset) error {
	if len(datasets) == 0 {
		return fmt.Errorf("management dataset inventory missing")
	}
	if e := l.verifyDatasetProperties(ctx, datasets); e != nil {
		return e
	}
	for _, ds := range datasets {
		if e := l.verifyMountedDataset(ctx, ds); e != nil {
			return e
		}
	}
	return nil
}
func (l *Linux) verifyMountedDataset(ctx context.Context, ds Dataset) error {
	raw, e := l.run(ctx, "findmnt", "-n", "-o", "SOURCE,FSTYPE", "--mountpoint", "/"+ds.Name)
	if e != nil {
		return e
	}
	fields := strings.Fields(raw)
	if len(fields) != 2 || fields[0] != ds.Name || fields[1] != "zfs" {
		return fmt.Errorf("management bind path /%s is not mounted from its expected ZFS dataset", ds.Name)
	}
	return nil
}
func verifyManagementBinds(doc map[string]any) error {
	for name, target := range map[string]string{"rqlite": "/rqlite/file", "victoria-metrics": "/victoria-metrics-data", "victoria-logs": "/victoria-logs-data", "floatlab-control": "/var/lib/floatlab/raft"} {
		dataset := name
		if name == "floatlab-control" {
			dataset = "raft"
		}
		expected := "/" + managementDataset + "/" + dataset
		found := false
		volumes, ok := services(doc)[name]["volumes"].([]any)
		if !ok {
			return fmt.Errorf("management service %s has no persistent state bind", name)
		}
		for _, volume := range volumes {
			var source, destination string
			switch v := volume.(type) {
			case string:
				parts := strings.Split(v, ":")
				if len(parts) >= 2 {
					source, destination = parts[0], parts[1]
				}
			case map[string]any:
				source, _ = v["source"].(string)
				destination, _ = v["target"].(string)
			}
			if destination == target {
				if source != expected {
					return fmt.Errorf("management service %s state must bind %s", name, expected)
				}
				found = true
			}
		}
		if !found {
			return fmt.Errorf("management service %s state bind missing", name)
		}
	}
	return nil
}
func (l *Linux) Authorize(ctx context.Context, s *Status) error {
	if !s.Authorized {
		return fmt.Errorf("startup not authorized")
	}
	b, e := os.ReadFile(composePath)
	if e != nil {
		return e
	}
	doc, e := composeDocument(b)
	if e != nil {
		return e
	}
	for name, svc := range services(doc) {
		svc["restart"] = s.Policies[name]
	}
	b, e = yaml.Marshal(doc)
	if e != nil {
		return e
	}
	if e = update.WriteFile(composePath, b, 0644); e != nil {
		return e
	}
	for name, policy := range s.Policies {
		ids, e := l.run(ctx, "docker", "ps", "-aq", "--filter", "label=com.docker.compose.project=floatlab-system", "--filter", "label=com.docker.compose.service="+name)
		if e != nil {
			return e
		}
		for _, id := range strings.Fields(ids) {
			if _, e = l.run(ctx, "docker", "update", "--restart="+policy, id); e != nil {
				return e
			}
		}
	}
	if e = os.Remove(filepath.Join(activationDir, "blocked")); e != nil && !os.IsNotExist(e) {
		return e
	}
	if e = os.Remove(filepath.Join(l.Dir, "management-blocked")); e != nil && !os.IsNotExist(e) {
		return e
	}
	_, e = l.run(ctx, "systemctl", "unmask", "--runtime", "floatlab-core-stack.service")
	return e
}
func (l *Linux) Cleanup(ctx context.Context, s *Status) error {
	if !operationID.MatchString(s.OperationID) {
		return fmt.Errorf("invalid operation ownership")
	}
	project := "floatlab-test-" + strings.ToLower(s.OperationID)
	testPath := filepath.Join(l.Dir, s.OperationID, "preflight.yml")
	if _, e := os.Stat(testPath); e == nil {
		if e = l.compose(ctx, testPath, project, "down", "--timeout", "30"); e != nil {
			return e
		}
	}
	if b, e := os.ReadFile(filepath.Join(l.Dir, s.OperationID, "sandbox.json")); e == nil {
		var resources map[string]string
		if e = json.Unmarshal(b, &resources); e != nil {
			return e
		}
		network, table := resources["network"], resources["table"]
		if network != project+"-isolated" || !regexp.MustCompile(`^floatlab_[a-f0-9]{10}$`).MatchString(table) {
			return fmt.Errorf("invalid sandbox cleanup identity")
		}
		if _, e = l.run(ctx, "docker", "network", "inspect", network); e == nil {
			if _, e = l.run(ctx, "docker", "network", "rm", network); e != nil {
				return e
			}
		}
		if _, e = l.run(ctx, "nft", "list", "table", "inet", table); e == nil {
			if _, e = l.run(ctx, "nft", "delete", "table", "inet", table); e != nil {
				return e
			}
		}
	}
	raw, e := l.run(ctx, "zfs", "list", "-H", "-r", "-t", "filesystem", "-o", "name", "floatlab/system/update-tests/"+s.OperationID)
	if e == nil {
		names := strings.Fields(raw)
		sort.Sort(sort.Reverse(sort.StringSlice(names)))
		for _, name := range names {
			if !(name == "floatlab/system/update-tests/"+s.OperationID || strings.HasPrefix(name, "floatlab/system/update-tests/"+s.OperationID+"/")) {
				return fmt.Errorf("unexpected clone ownership")
			}
			if _, e = l.run(ctx, "zfs", "destroy", name); e != nil {
				return e
			}
		}
	}
	for _, name := range []string{"floatlab-preflight-" + s.OperationID, s.Snapshot} {
		if name == "" {
			continue
		}
		if name == s.Snapshot {
			if _, e = l.run(ctx, "zfs", "release", "-r", "floatlab-update", managementDataset+"@"+name); e != nil {
				return e
			}
		}
		if _, e = l.run(ctx, "zfs", "destroy", "-r", managementDataset+"@"+name); e != nil {
			if name == s.Snapshot {
				return e
			}
		}
	}
	return nil
}
