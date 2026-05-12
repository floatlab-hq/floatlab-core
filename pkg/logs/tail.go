package logs

import (
	"context"
	"io"
	"net/http"
	"time"
)

// ProxyTail proxies VictoriaLogs SSE tail to the HTTP response writer.
// It sets the correct Content-Type and streams until ctx is done or the
// upstream connection drops.
func ProxyTail(ctx context.Context, c *Client, w http.ResponseWriter, query string) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	hc := &http.Client{Timeout: 0}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.TailURL(query), nil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	resp, err := hc.Do(req)
	if err != nil {
		_, _ = w.Write([]byte(": upstream unavailable\n\n"))
		flusher.Flush()
		return
	}
	defer resp.Body.Close()

	buf := make([]byte, 4096)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if _, writeErr := w.Write(buf[:n]); writeErr != nil {
				return
			}
			flusher.Flush()
		}
		if readErr != nil {
			if readErr != io.EOF {
				time.Sleep(500 * time.Millisecond)
			}
			return
		}
	}
}
