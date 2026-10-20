package main

import (
	"context"
	"go.uber.org/zap"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestControllerDrainAcknowledgesExactOperation(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("FLOATLAB_ACTIVATION_DIR", dir)
	if e := os.WriteFile(filepath.Join(dir, "blocked"), []byte("op-123"), 0600); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go superviseControllers(ctx, func(context.Context) { t.Error("blocked controllers started") }, zap.NewNop())
	deadline := time.Now().Add(time.Second)
	for {
		b, e := os.ReadFile(filepath.Join(dir, "controllers-drained"))
		if e == nil {
			if string(b) != "op-123" {
				t.Fatalf("drain token %q", b)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("drain not acknowledged")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestSidecarChecksRequireStoredTokenAndValue(t *testing.T) {
	for _, b := range []string{`{}`, `{"status":"success","data":{"resultType":"vector","result":[]}}`, `{"status":"success","data":{"resultType":"vector","result":[{"metric":{"token":"different"},"value":[1,"1"]}]}}`, `{"status":"success","data":{"resultType":"vector","result":[{"metric":{"token":"test"},"value":[1,"0"]}]}}`} {
		if e := checkMetricSample([]byte(b), "test"); e == nil {
			t.Fatalf("accepted missing/wrong sample %s", b)
		}
	}
	if e := checkMetricSample([]byte(`{"status":"success","data":{"resultType":"vector","result":[{"metric":{"token":"test"},"value":[1,"1"]}]}}`), "test"); e != nil {
		t.Fatal(e)
	}
	for _, b := range []string{"", `{"token":"wrong","_msg":"preflight-test"}`, `{"token":"test","_msg":"wrong"}`} {
		if e := checkLogSample([]byte(b), "test"); e == nil {
			t.Fatalf("accepted missing/wrong log %s", b)
		}
	}
	if e := checkLogSample([]byte("{\"token\":\"test\",\"_msg\":\"preflight-test\"}\n"), "test"); e != nil {
		t.Fatal(e)
	}
}
