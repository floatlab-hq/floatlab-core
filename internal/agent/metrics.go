package agent

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/floatlab/floatlab-core/pkg/store"
)

type MetricsForwarder struct {
	NodeID string
	URL    string
	ZFS    ZFS
	Client *http.Client
	Logger *slog.Logger
}

func (m MetricsForwarder) Run(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 5 * time.Second
	}
	m.collectAndPush(ctx)
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.collectAndPush(ctx)
		}
	}
}

func (m MetricsForwarder) collectAndPush(ctx context.Context) {
	body, err := m.Collect(ctx)
	if err != nil {
		m.logger().Warn("collect metrics failed", "err", err)
		return
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(m.URL, "/")+"/api/v1/import/prometheus", strings.NewReader(body))
	if err != nil {
		m.logger().Warn("create metrics request failed", "err", err)
		return
	}
	req.Header.Set("Content-Type", "text/plain; version=0.0.4")
	resp, err := m.client().Do(req)
	if err != nil {
		m.logger().Warn("push metrics failed", "err", err)
		return
	}
	defer drainAndClose(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		m.logger().Warn("push metrics failed", "status", resp.Status)
	}
}

func (m MetricsForwarder) Collect(ctx context.Context) (string, error) {
	datasets, err := m.ZFS.ListDatasets(ctx)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for _, ds := range datasets {
		labels := labels(map[string]string{"node_id": m.NodeID, "dataset": ds.Name, "pool": poolName(ds.Name), "type": ds.Type})
		metric(&b, "fl_zfs_dataset_used_bytes", labels, float64(ds.UsedBytes))
		metric(&b, "fl_zfs_dataset_available_bytes", labels, float64(ds.AvailableBytes))
		metric(&b, "fl_zfs_dataset_referenced_bytes", labels, float64(ds.ReferencedBytes))
		metric(&b, "fl_zfs_dataset_snapshot_used_bytes", labels, float64(ds.SnapshotUsedBytes))
		if ds.QuotaBytes != nil {
			metric(&b, "fl_zfs_dataset_quota_bytes", labels, float64(*ds.QuotaBytes))
		}
	}
	for _, p := range uniquePools(datasets) {
		ioStat, err := readPoolIO("/proc/spl/kstat/zfs", p)
		if err != nil {
			m.logger().Warn("read zfs pool iostats failed", "pool", p, "err", err)
			continue
		}
		labels := labels(map[string]string{"node_id": m.NodeID, "pool": p})
		metric(&b, "fl_zfs_pool_read_operations_total", labels, float64(ioStat.ReadOps))
		metric(&b, "fl_zfs_pool_write_operations_total", labels, float64(ioStat.WriteOps))
		metric(&b, "fl_zfs_pool_read_bytes_total", labels, float64(ioStat.ReadBytes))
		metric(&b, "fl_zfs_pool_write_bytes_total", labels, float64(ioStat.WriteBytes))
	}
	diskstats, err := os.ReadFile("/proc/diskstats")
	if err == nil {
		for _, ds := range parseDiskstats(string(diskstats)) {
			labels := labels(map[string]string{"node_id": m.NodeID, "device": ds.Device, "major": ds.Major, "minor": ds.Minor})
			metric(&b, "fl_disk_reads_completed_total", labels, float64(ds.Reads))
			metric(&b, "fl_disk_writes_completed_total", labels, float64(ds.Writes))
			metric(&b, "fl_disk_read_bytes_total", labels, float64(ds.ReadSectors*512))
			metric(&b, "fl_disk_write_bytes_total", labels, float64(ds.WriteSectors*512))
			metric(&b, "fl_disk_read_seconds_total", labels, float64(ds.ReadMS)/1000)
			metric(&b, "fl_disk_write_seconds_total", labels, float64(ds.WriteMS)/1000)
			metric(&b, "fl_disk_io_now", labels, float64(ds.InFlight))
			metric(&b, "fl_disk_io_seconds_total", labels, float64(ds.IOMS)/1000)
			metric(&b, "fl_disk_io_weighted_seconds_total", labels, float64(ds.WeightedIOMS)/1000)
			if ds.HasDiscard {
				metric(&b, "fl_disk_discards_completed_total", labels, float64(ds.Discards))
				metric(&b, "fl_disk_discard_bytes_total", labels, float64(ds.DiscardSectors*512))
				metric(&b, "fl_disk_discard_seconds_total", labels, float64(ds.DiscardMS)/1000)
			}
			if ds.HasFlush {
				metric(&b, "fl_disk_flush_requests_total", labels, float64(ds.Flushes))
				metric(&b, "fl_disk_flush_seconds_total", labels, float64(ds.FlushMS)/1000)
			}
		}
	} else {
		m.logger().Warn("read diskstats failed", "err", err)
	}
	return b.String(), nil
}

func (m MetricsForwarder) client() *http.Client {
	if m.Client != nil {
		return m.Client
	}
	return &http.Client{Timeout: 4 * time.Second}
}

func (m MetricsForwarder) logger() *slog.Logger {
	if m.Logger != nil {
		return m.Logger
	}
	return slog.Default()
}

type poolIO struct {
	ReadOps, WriteOps, ReadBytes, WriteBytes uint64
}

func readPoolIO(root, pool string) (poolIO, error) {
	b, err := os.ReadFile(filepath.Join(root, pool, "iostats"))
	if err != nil {
		return poolIO{}, err
	}
	return parsePoolIO(string(b))
}

func parsePoolIO(s string) (poolIO, error) {
	var out poolIO
	for _, line := range strings.Split(s, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		if fields[0] == "reads" || fields[0] == "read_ops" {
			out.ReadOps, _ = strconv.ParseUint(fields[len(fields)-1], 10, 64)
		}
		if fields[0] == "writes" || fields[0] == "write_ops" {
			out.WriteOps, _ = strconv.ParseUint(fields[len(fields)-1], 10, 64)
		}
		if fields[0] == "nread" || fields[0] == "read_bytes" {
			out.ReadBytes, _ = strconv.ParseUint(fields[len(fields)-1], 10, 64)
		}
		if fields[0] == "nwritten" || fields[0] == "write_bytes" {
			out.WriteBytes, _ = strconv.ParseUint(fields[len(fields)-1], 10, 64)
		}
		if len(fields) >= 13 && fields[0] != "name" {
			values := fields[len(fields)-4:]
			out.ReadBytes, _ = strconv.ParseUint(values[0], 10, 64)
			out.WriteBytes, _ = strconv.ParseUint(values[1], 10, 64)
			out.ReadOps, _ = strconv.ParseUint(values[2], 10, 64)
			out.WriteOps, _ = strconv.ParseUint(values[3], 10, 64)
		}
	}
	return out, nil
}

type diskStat struct {
	Major, Minor              string
	Device                    string
	Reads, Writes             uint64
	ReadSectors, WriteSectors uint64
	ReadMS, WriteMS           uint64
	InFlight                  uint64
	IOMS, WeightedIOMS        uint64
	HasDiscard, HasFlush      bool
	Discards, DiscardSectors  uint64
	DiscardMS                 uint64
	Flushes, FlushMS          uint64
}

func parseDiskstats(s string) []diskStat {
	var out []diskStat
	for _, line := range strings.Split(s, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 14 {
			continue
		}
		nums := make([]uint64, len(fields)-3)
		ok := true
		for i, field := range fields[3:] {
			n, err := strconv.ParseUint(field, 10, 64)
			if err != nil {
				ok = false
				break
			}
			nums[i] = n
		}
		if !ok {
			continue
		}
		stat := diskStat{
			Major: fields[0], Minor: fields[1], Device: fields[2],
			Reads: nums[0], ReadSectors: nums[2], ReadMS: nums[3],
			Writes: nums[4], WriteSectors: nums[6], WriteMS: nums[7],
			InFlight: nums[8], IOMS: nums[9], WeightedIOMS: nums[10],
		}
		if len(nums) >= 15 {
			stat.HasDiscard = true
			stat.Discards, stat.DiscardSectors, stat.DiscardMS = nums[11], nums[13], nums[14]
		}
		if len(nums) >= 17 {
			stat.HasFlush = true
			stat.Flushes, stat.FlushMS = nums[15], nums[16]
		}
		out = append(out, stat)
	}
	return out
}

func uniquePools(datasets []store.Dataset) []string {
	seen := map[string]bool{}
	for _, ds := range datasets {
		seen[poolName(ds.Name)] = true
	}
	out := make([]string, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

func poolName(dataset string) string {
	if i := strings.IndexByte(dataset, '/'); i >= 0 {
		return dataset[:i]
	}
	return dataset
}

func metric(b *strings.Builder, name, labels string, value float64) {
	fmt.Fprintf(b, "%s%s %s\n", name, labels, strconv.FormatFloat(value, 'f', -1, 64))
}

func labels(values map[string]string) string {
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteByte('{')
	for i, key := range keys {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, "%s=%q", key, values[key])
	}
	b.WriteByte('}')
	return b.String()
}
