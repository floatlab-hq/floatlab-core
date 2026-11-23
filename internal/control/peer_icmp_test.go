package control

import (
	"context"
	"errors"
	"syscall"
	"testing"
	"time"
)

func TestPeerICMPLoopback(t *testing.T) {
	for _, address := range []string{"127.0.0.1", "::1"} {
		t.Run(address, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			result, err := probePeerICMP(ctx, address)
			if errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.EAFNOSUPPORT) {
				t.Skipf("ICMP socket unavailable: %v", err)
			}
			if err != nil {
				t.Fatal(err)
			}
			if result.lossPercent() != 0 || result.rtt <= 0 {
				t.Fatalf("expected loopback echo replies, got %+v", result)
			}
		})
	}
}
