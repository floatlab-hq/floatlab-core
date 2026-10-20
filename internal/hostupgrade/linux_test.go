package hostupgrade

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

type pool struct {
	datasets          map[string]map[string]string
	bootfs            string
	mutations, failAt int
}

func newPool() *pool {
	p := &pool{datasets: map[string]map[string]string{}, bootfs: "floatlab/os/current"}
	p.version("1.0.0")
	p.version("2.0.0")
	p.datasets["floatlab/os/current"] = bootProperties("1.0.0")
	p.datasets["floatlab/os/next"] = bootProperties("2.0.0")
	return p
}
func bootProperties(version string) map[string]string {
	return map[string]string{"origin": "floatlab/os/" + version + "@base", "readonly": "on", "org.floatlab:host-id": "12345678", "org.floatlab:version": version, "org.floatlab:init": "/nix/store/test/init", "org.floatlab:update": "op", "mounted": "no"}
}
func (p *pool) version(version string) {
	p.datasets["floatlab/os/"+version] = map[string]string{"readonly": "on", "org.floatlab:update": "op"}
	p.datasets["floatlab/os/"+version+"@base"] = map[string]string{}
}
func (p *pool) run(_ context.Context, name string, args ...string) (string, error) {
	if name == "zpool" {
		if args[0] == "set" {
			if err := p.mutate(); err != nil {
				return "", err
			}
			p.bootfs = strings.TrimPrefix(args[1], "bootfs=")
			return "", p.validBoot()
		}
		return "", nil
	}
	if name != "zfs" {
		return "", fmt.Errorf("unexpected command %s", name)
	}
	last := args[len(args)-1]
	switch args[0] {
	case "get":
		ds, ok := p.datasets[last]
		if !ok {
			return "", errors.New("not found")
		}
		return ds[args[len(args)-2]], nil
	case "list":
		if strings.Contains(strings.Join(args, " "), "-d 1") {
			names := []string{}
			for name := range p.datasets {
				if strings.HasPrefix(name, last+"/") && !strings.Contains(name, "@") {
					names = append(names, name)
				}
			}
			return strings.Join(names, "\n"), nil
		}
		if _, ok := p.datasets[last]; !ok {
			return "", errors.New("not found")
		}
		return last, nil
	case "clone":
		if err := p.mutate(); err != nil {
			return "", err
		}
		p.datasets[last] = map[string]string{"origin": args[len(args)-2]}
		for i := 1; i < len(args)-2; i++ {
			if args[i] == "-o" {
				pair := strings.SplitN(args[i+1], "=", 2)
				p.datasets[last][pair[0]] = pair[1]
				i++
			}
		}
		return "", nil
	case "set":
		if err := p.mutate(); err != nil {
			return "", err
		}
		ds, ok := p.datasets[last]
		if !ok {
			return "", errors.New("not found")
		}
		for _, arg := range args[1 : len(args)-1] {
			parts := strings.SplitN(arg, "=", 2)
			ds[parts[0]] = parts[1]
		}
		return "", nil
	case "rename":
		if err := p.mutate(); err != nil {
			return "", err
		}
		from := args[1]
		if _, ok := p.datasets[last]; ok {
			return "", errors.New("destination exists")
		}
		ds, ok := p.datasets[from]
		if !ok {
			return "", errors.New("source missing")
		}
		p.datasets[last] = ds
		delete(p.datasets, from)
		return "", p.validBoot()
	case "destroy":
		if err := p.mutate(); err != nil {
			return "", err
		}
		if p.bootfs == last {
			return "", errors.New("destroying bootfs")
		}
		for name, ds := range p.datasets {
			if ds["origin"] == last || strings.HasPrefix(name, last+"@") {
				return "", errors.New("snapshot or dependent clone prevents destruction")
			}
		}
		delete(p.datasets, last)
		return "", nil
	}
	return "", fmt.Errorf("unexpected ZFS command %v", args)
}
func (p *pool) mutate() error {
	p.mutations++
	if p.failAt == p.mutations {
		return errors.New("power interrupted")
	}
	return nil
}
func (p *pool) validBoot() error {
	ds, ok := p.datasets[p.bootfs]
	if !ok {
		return fmt.Errorf("bootfs %s missing", p.bootfs)
	}
	if ds["readonly"] != "on" || ds["org.floatlab:host-id"] != "12345678" || ds["org.floatlab:version"] == "" || ds["org.floatlab:init"] == "" {
		return fmt.Errorf("bootfs %s lacks validated identity/init", p.bootfs)
	}
	return nil
}
func promotionStatus(source, target string) Status {
	old := manifest(source)
	old.InstalledHostID = "12345678"
	return Status{OperationID: "op", Source: old, Manifest: manifest(target)}
}

func TestEveryPromotionMutationKeepsBootableTargetAndCanResume(t *testing.T) {
	baseline := newPool()
	if err := (Linux{Run: baseline.run}).Promote(context.Background(), promotionStatus("1.0.0", "2.0.0")); err != nil {
		t.Fatal(err)
	}
	for fail := 1; fail <= baseline.mutations; fail++ {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			p := newPool()
			p.failAt = fail
			l := Linux{Run: p.run}
			_ = l.Promote(context.Background(), promotionStatus("1.0.0", "2.0.0"))
			if err := p.validBoot(); err != nil {
				t.Fatal(err)
			}
			p.failAt = 0
			if err := l.Promote(context.Background(), promotionStatus("1.0.0", "2.0.0")); err != nil {
				t.Fatal(err)
			}
			if p.bootfs != "floatlab/os/current" || p.datasets[p.bootfs]["origin"] != "floatlab/os/2.0.0@base" {
				t.Fatal(p.bootfs, p.datasets)
			}
		})
	}
}

func TestSecondPromotionRefreshesRecoveryAndRetainsPriorVersionUntilCommit(t *testing.T) {
	for fail := 1; fail <= 12; fail++ {
		p := newPool()
		l := Linux{Run: p.run}
		if err := l.Promote(context.Background(), promotionStatus("1.0.0", "2.0.0")); err != nil {
			t.Fatal(err)
		}
		p.version("3.0.0")
		p.datasets["floatlab/os/next"] = bootProperties("3.0.0")
		p.mutations = 0
		p.failAt = fail
		s := promotionStatus("2.0.0", "3.0.0")
		s.OperationID = "second"
		_ = l.Promote(context.Background(), s)
		if err := p.validBoot(); err != nil {
			t.Fatalf("failure %d: %v", fail, err)
		}
		if p.bootfs == "floatlab/os/recovery" && p.datasets[p.bootfs]["origin"] != "floatlab/os/2.0.0@base" {
			t.Fatalf("stale fallback after mutation %d", fail)
		}
		p.failAt = 0
		if err := l.Promote(context.Background(), s); err != nil {
			t.Fatalf("failure %d: %v", fail, err)
		}
		if p.datasets["floatlab/os/previous"]["origin"] != "floatlab/os/2.0.0@base" {
			t.Fatal(p.datasets)
		}
		if p.datasets["floatlab/os/retired-second"]["origin"] != "floatlab/os/1.0.0@base" {
			t.Fatal("older retained alias lost")
		}
	}
}

func TestFallbackRepairsInterruptedPromotionBeforeManagementRestore(t *testing.T) {
	p := newPool()
	l := Linux{Run: p.run}
	s := promotionStatus("1.0.0", "2.0.0")
	if err := l.Promote(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	if err := l.RestoreBoot(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	if p.bootfs != "floatlab/os/current" || p.datasets[p.bootfs]["origin"] != "floatlab/os/1.0.0@base" {
		t.Fatal(p.datasets)
	}
}

func TestForeignRecoveryAliasIsNeverDestroyed(t *testing.T) {
	p := newPool()
	p.datasets["floatlab/os/recovery"] = bootProperties("0.9.0")
	delete(p.datasets["floatlab/os/recovery"], "org.floatlab:update")
	err := (Linux{Run: p.run}).Promote(context.Background(), promotionStatus("1.0.0", "2.0.0"))
	if err == nil {
		t.Fatal("foreign alias accepted")
	}
	if _, ok := p.datasets["floatlab/os/recovery"]; !ok {
		t.Fatal("foreign alias destroyed")
	}
}

func TestTrialFirmwareEntriesAreReusedForEachInstalledESP(t *testing.T) {
	entries := ""
	created := 0
	l := Linux{Run: func(_ context.Context, name string, args ...string) (string, error) {
		if name != "efibootmgr" {
			return "", fmt.Errorf("unexpected command")
		}
		if args[0] == "-v" {
			return entries, nil
		}
		if args[0] != "--create-only" {
			return "", fmt.Errorf("BootOrder must not change")
		}
		created++
		partition := "guid-one"
		if args[2] == "/dev/disk/by-id/two" {
			partition = "guid-two"
		}
		entries += fmt.Sprintf("Boot%04X* FloatLab Trial HD(1,GPT,%s,0,1)/File(\\EFI\\FloatLab\\TRIAL.EFI)\n", created, partition)
		return "", nil
	}}
	for round := 0; round < 2; round++ {
		for _, disk := range []struct{ path, id string }{{"/dev/disk/by-id/one", "guid-one"}, {"/dev/disk/by-id/two", "guid-two"}} {
			if _, err := l.ensureTrialEntry(context.Background(), disk.path, disk.id); err != nil {
				t.Fatal(err)
			}
		}
	}
	if created != 2 {
		t.Fatalf("created %d entries across two activation cycles", created)
	}
}

func TestCleanupPreservesCurrentPreviousLiveAndExternalCloneOrigins(t *testing.T) {
	p := newPool()
	p.version("0.5.0")
	p.version("0.9.0")
	p.version("4.0.0")
	p.datasets["floatlab/os/current"] = bootProperties("2.0.0")
	p.datasets["floatlab/os/previous"] = bootProperties("1.0.0")
	p.datasets["floatlab/os/live"] = bootProperties("0.5.0")
	p.datasets["floatlab/os/retired-op"] = bootProperties("0.9.0")
	delete(p.datasets, "floatlab/os/next")
	p.datasets["foreign/pinned"] = map[string]string{"origin": "floatlab/os/4.0.0@base"}
	s := promotionStatus("1.0.0", "2.0.0")
	s.State = "committed"
	err := (Linux{Run: p.run}).Cleanup(context.Background(), s)
	if err == nil {
		t.Fatal("external clone ignored")
	}
	for _, version := range []string{"0.5.0", "1.0.0", "2.0.0", "4.0.0"} {
		if _, ok := p.datasets["floatlab/os/"+version+"@base"]; !ok {
			t.Fatal("protected origin removed", version)
		}
	}
	if _, ok := p.datasets["floatlab/os/retired-op"]; ok {
		t.Fatal("retired alias not reclaimed")
	}
}
