package hostd

import (
	"context"
	"os"
	"sync"
	"time"
)

func (s *Server) superviseTelemetry(ctx context.Context) {
	var cancel context.CancelFunc
	var done chan struct{}
	defer func() {
		if cancel != nil {
			cancel()
			<-done
		}
	}()
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		_, err := os.Stat("/run/floatlab/activation/blocked")
		blocked := err == nil || !os.IsNotExist(err)
		if blocked && cancel != nil {
			cancel()
			<-done
			cancel = nil
		}
		if !blocked && cancel == nil {
			child, c := context.WithCancel(ctx)
			cancel = c
			done = make(chan struct{})
			go func() {
				defer close(done)
				var wg sync.WaitGroup
				wg.Add(1)
				go func() { defer wg.Done(); s.forwardJournal(child) }()
				if s.dispatcher.docker != nil {
					wg.Add(1)
					go func() {
						defer wg.Done()
						s.dispatcher.docker.ForwardTelemetry(child, telemetryEnv("FLOATLAB_NODE_ID", "node1"), telemetryEnv("FLOATLAB_VLOGS_URL", "http://127.0.0.1:9428"), telemetryEnv("FLOATLAB_VMETRICS_URL", "http://127.0.0.1:8428"), s.log)
					}()
				}
				wg.Wait()
			}()
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
