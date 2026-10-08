package control

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/floatlab/floatlab-core/pkg/notify"
	"go.uber.org/zap"
)

func TestSSEThroughMiddlewareHasIndependentSubscribers(t *testing.T) {
	s := &Server{cfg: &Config{JWTSecret: "sse-test"}, broker: notify.NewBroker(), log: zap.NewNop(), auth: newAuthLimiter()}
	server := httptest.NewServer(s.buildRouter())
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	readers := []*bufio.Reader{}
	for range 2 {
		request, _ := http.NewRequestWithContext(ctx, "GET", server.URL+"/api/v1/events", nil)
		request.Header.Set("Authorization", "Bearer "+signedToken(t, "sse-test", []string{"admin"}))
		response, err := server.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		if response.StatusCode != 200 {
			t.Fatalf("SSE status %s", response.Status)
		}
		reader := bufio.NewReader(response.Body)
		if line, err := reader.ReadString('\n'); err != nil || !strings.Contains(line, "connected") {
			t.Fatalf("initial SSE flush: %q %v", line, err)
		}
		readers = append(readers, reader)
	}
	s.broker.PublishJSON("test", []byte(`{"id":"independent"}`))
	for _, reader := range readers {
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(line, "independent") {
				break
			}
		}
	}
}
