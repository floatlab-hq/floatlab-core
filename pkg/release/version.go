package release

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/floatlab/floatlab-core/pkg/ipc"
	"github.com/hashicorp/go-version"
)

// Version and GitCommit identify the running binary, supplied by release builds.
var Version = "0.1.0-dev"
var GitCommit = "unknown"

//go:embed compatibility.json
var compatibilityJSON []byte

type HostRange struct {
	Min string `json:"min_host_version"`
	Max string `json:"max_host_version"`
}

var RequiredHost = func() HostRange {
	var r HostRange
	if err := json.Unmarshal(compatibilityJSON, &r); err != nil {
		panic(err)
	}
	if err := r.Validate(); err != nil {
		panic(err)
	}
	return r
}()

var semver = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-([0-9A-Za-z-]+\.)*[0-9A-Za-z-]+)?$`)
var numericLeadingZero = regexp.MustCompile(`^0[0-9]+$`)

func Parse(s string) (*version.Version, error) {
	if !semver.MatchString(s) {
		return nil, fmt.Errorf("invalid release version %q", s)
	}
	v, err := version.NewSemver(s)
	if err != nil {
		return nil, err
	}
	for _, part := range strings.Split(v.Prerelease(), ".") {
		if numericLeadingZero.MatchString(part) {
			return nil, fmt.Errorf("invalid numeric prerelease %q", part)
		}
	}
	return v, nil
}

func (r HostRange) Validate() error {
	min, err := Parse(r.Min)
	if err != nil {
		return err
	}
	max, err := Parse(r.Max)
	if err != nil {
		return err
	}
	if !min.LessThan(max) {
		return fmt.Errorf("host minimum must be less than maximum")
	}
	return nil
}

func (r HostRange) Contains(s string) (bool, error) {
	if err := r.Validate(); err != nil {
		return false, err
	}
	v, err := Parse(s)
	if err != nil {
		return false, err
	}
	min, _ := Parse(r.Min)
	max, _ := Parse(r.Max)
	return !v.LessThan(min) && v.LessThan(max), nil
}

type Compatibility struct {
	Status      string    `json:"status"`
	HostVersion string    `json:"host_version,omitempty"`
	GitCommit   string    `json:"git_commit,omitempty"`
	Required    HostRange `json:"required"`
	Error       string    `json:"error,omitempty"`
}

type HostExecutor interface {
	Execute(context.Context, string, string, any) (json.RawMessage, error)
}

func CheckHost(ctx context.Context, hosts HostExecutor, node string) Compatibility {
	result := Compatibility{Status: "unknown", Required: RequiredHost}
	raw, err := hosts.Execute(ctx, node, "sys.version", nil)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	var info ipc.VersionResult
	if err = json.Unmarshal(raw, &info); err != nil {
		result.Error = err.Error()
		return result
	}
	result.HostVersion, result.GitCommit = info.HostVersion, info.GitCommit
	ok, err := RequiredHost.Contains(info.HostVersion)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	result.Status = "incompatible"
	if ok {
		result.Status = "compatible"
	}
	return result
}
