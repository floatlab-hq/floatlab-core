package docker

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/compose-spec/compose-go/v2/types"
	composeapi "github.com/docker/compose/v5/pkg/api"
	dockerclient "github.com/moby/moby/client"
)

func TestExecDockerAPI(t *testing.T) {
	for _, scenario := range []string{"success", "foreign", "overflow", "timeout"} {
		t.Run(scenario, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case strings.HasSuffix(r.URL.Path, "/containers/web/json"):
					project := "stack"
					if scenario == "foreign" {
						project = "other"
					}
					json.NewEncoder(w).Encode(map[string]any{"Config": map[string]any{"Labels": map[string]string{LabelComposeProject: project}}})
				case strings.HasSuffix(r.URL.Path, "/containers/web/exec"):
					var body struct {
						Cmd              []string
						Tty, AttachStdin bool
					}
					json.NewDecoder(r.Body).Decode(&body)
					if body.Tty || body.AttachStdin || strings.Join(body.Cmd, ",") != "echo,a b,$literal" {
						t.Errorf("unexpected exec create: %+v", body)
					}
					io.WriteString(w, `{"Id":"exec-id"}`)
				case strings.HasSuffix(r.URL.Path, "/exec/exec-id/start"):
					io.Copy(io.Discard, r.Body)
					connection, buffer, err := w.(http.Hijacker).Hijack()
					if err != nil {
						t.Error(err)
						return
					}
					defer connection.Close()
					buffer.WriteString("HTTP/1.1 101 UPGRADED\r\nContent-Type: application/vnd.docker.raw-stream\r\nConnection: Upgrade\r\nUpgrade: tcp\r\n\r\n")
					buffer.Flush()
					if scenario == "timeout" {
						time.Sleep(150 * time.Millisecond)
						return
					}
					stdout := "out"
					if scenario == "overflow" {
						stdout = strings.Repeat("x", ExecOutputLimit+1)
					}
					for stream, data := range map[byte]string{1: stdout, 2: "err"} {
						header := make([]byte, 8)
						header[0] = stream
						binary.BigEndian.PutUint32(header[4:], uint32(len(data)))
						connection.Write(header)
						connection.Write([]byte(data))
					}
				case strings.HasSuffix(r.URL.Path, "/exec/exec-id/json"):
					io.WriteString(w, `{"ID":"exec-id","Running":false,"ExitCode":7}`)
				default:
					t.Errorf("unexpected request %s", r.URL.Path)
					w.WriteHeader(404)
				}
			}))
			defer server.Close()
			dc, err := dockerclient.NewClientWithOpts(dockerclient.WithHost("tcp://"+strings.TrimPrefix(server.URL, "http://")), dockerclient.WithVersion("1.55"))
			if err != nil {
				t.Fatal(err)
			}
			defer dc.Close()
			ctx := context.Background()
			if scenario == "timeout" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 100*time.Millisecond)
				defer cancel()
			}
			result, err := (&Client{dc: dc}).Exec(ctx, "stack", "web", []string{"echo", "a b", "$literal"})
			if scenario == "success" {
				if err != nil || result.Stdout != "out" || result.Stderr != "err" || result.ExitCode != 7 {
					t.Fatalf("result=%+v error=%v", result, err)
				}
			} else {
				expected := map[string]string{"foreign": "does not belong", "overflow": "1 MiB", "timeout": "timed out"}[scenario]
				if err == nil || !strings.Contains(err.Error(), expected) {
					t.Fatalf("expected %q error, got %v", expected, err)
				}
			}
		})
	}
}

// A failed provision may leave no saved Compose file, but deletion must still
// remove any resources Docker knows about under the stack's project label.
func TestComposeDownMissingSource(t *testing.T) {
	for _, loadErr := range []error{os.ErrNotExist, errors.New("invalid compose")} {
		service := &downCompose{loadErr: loadErr}
		client := &Client{compose: service}
		err := client.ComposeDown(context.Background(), "stack", "/missing.yaml", false)
		if errors.Is(loadErr, os.ErrNotExist) {
			if err != nil || !service.called {
				t.Fatalf("missing source: called=%v error=%v", service.called, err)
			}
		} else if !errors.Is(err, loadErr) || service.called {
			t.Fatalf("invalid source: called=%v error=%v", service.called, err)
		}
	}
}

type downCompose struct {
	composeapi.Compose
	loadErr error
	called  bool
}

func (s *downCompose) LoadProject(context.Context, composeapi.ProjectLoadOptions) (*types.Project, error) {
	return nil, s.loadErr
}
func (s *downCompose) Down(_ context.Context, project string, options composeapi.DownOptions) error {
	s.called = project == "stack" && options.Project == nil && options.RemoveOrphans && !options.Volumes
	return nil
}
