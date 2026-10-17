package release

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func TestHostRange(t *testing.T) {
	r := HostRange{Min: "0.1.0", Max: "0.2.0"}
	for _, tc := range []struct {
		version string
		want    bool
	}{
		{"0.1.0", true}, {"0.1.9", true}, {"0.2.0", false}, {"0.0.9", false},
		{"0.1.0-rc.1", false}, {"0.2.0-rc.1", true},
	} {
		got, err := r.Contains(tc.version)
		if err != nil || got != tc.want {
			t.Fatalf("%s: %v, %v", tc.version, got, err)
		}
	}
	for _, invalid := range []string{"", "dev", "v0.1.0", "0.1", "0.01.0", "0.1.0-01"} {
		if _, err := r.Contains(invalid); err == nil {
			t.Fatalf("accepted %q", invalid)
		}
	}
	if err := (HostRange{Min: "0.2.0", Max: "0.1.0"}).Validate(); err == nil {
		t.Fatal("accepted reversed range")
	}
	if err := RequiredHost.Validate(); err != nil {
		t.Fatal(err)
	}
}

type hostReply struct {
	raw string
	err error
}

func (h hostReply) Execute(_ context.Context, _, command string, _ any) (json.RawMessage, error) {
	if command != "sys.version" {
		panic(command)
	}
	return json.RawMessage(h.raw), h.err
}

func TestCheckHost(t *testing.T) {
	for _, tc := range []struct {
		reply hostReply
		want  string
	}{
		{hostReply{raw: `{"host_version":"0.1.0","git_commit":"abc"}`}, "compatible"},
		{hostReply{raw: `{"host_version":"0.2.0"}`}, "incompatible"},
		{hostReply{raw: `{}`}, "unknown"},
		{hostReply{raw: `invalid`}, "unknown"},
		{hostReply{err: errors.New("unknown command")}, "unknown"},
	} {
		if got := CheckHost(context.Background(), tc.reply, "node"); got.Status != tc.want {
			t.Fatalf("%+v: %+v", tc, got)
		}
	}
}
