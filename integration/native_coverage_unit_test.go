package integration_test

import (
	"reflect"
	"testing"
)

func TestCoverageRetainsOnlyLatestCheckpointPerProcess(t *testing.T) {
	paths := []string{
		"/floatlab/system/integration-coverage/covcounters.hash.1.300",
		"/floatlab/system/integration-coverage/boot-control-1-10/covmeta.hash",
		"/floatlab/system/integration-coverage/boot-control-1-10/covcounters.hash.1.100",
		"/floatlab/system/integration-coverage/boot-control-1-10/covcounters.hash.1.200",
		"/floatlab/system/integration-coverage/boot-control-1-20/covmeta.hash",
		"/floatlab/system/integration-coverage/boot-control-1-20/covcounters.hash.1.250",
	}
	got, err := latestCoverageFiles(paths)
	want := []string{paths[3], paths[1], paths[5], paths[4]}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("files=%v want=%v error=%v", got, want, err)
	}
	if _, err := latestCoverageFiles(nil); err == nil {
		t.Fatal("empty appliance coverage accepted")
	}
}
