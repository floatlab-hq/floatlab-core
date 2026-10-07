package logs

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestProxyTailTransformsVictoriaLogsLineToSSE(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"_time":"2026-01-01T00:00:00Z","_msg":"ready","_stream_fields":{"stream":"stderr"}}` + "\n"))
	}))
	defer upstream.Close()

	response := httptest.NewRecorder()
	ProxyTail(t.Context(), NewClient(upstream.URL), response, `container_id:"demo"`, func(line LogLine) interface{} {
		return map[string]string{"ts": line.Time, "stream": line.Stream["stream"], "msg": line.Msg}
	})
	if response.Header().Get("Content-Type") != "text/event-stream" || !strings.Contains(response.Body.String(), `data: {"msg":"ready","stream":"stderr","ts":"2026-01-01T00:00:00Z"}`) {
		t.Fatalf("unexpected SSE response: %q", response.Body.String())
	}
}
