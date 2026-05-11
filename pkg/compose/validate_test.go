package compose

import (
	"testing"
)

func TestValidate_MissingPrimaryNode(t *testing.T) {
	ps := &ParsedStack{
		Extension: StackExtension{
			Storage: StorageConfig{Pool: "floatlab"},
		},
	}
	if err := Validate(ps); err == nil {
		t.Error("expected error for missing primary_node")
	}
}

func TestValidate_MissingPool(t *testing.T) {
	ps := &ParsedStack{
		Extension: StackExtension{PrimaryNode: "node-alpha"},
	}
	if err := Validate(ps); err == nil {
		t.Error("expected error for missing storage.pool")
	}
}

func TestValidate_InvalidBlockSize(t *testing.T) {
	ps := &ParsedStack{
		Extension: StackExtension{
			PrimaryNode: "node-alpha",
			Storage:     StorageConfig{Pool: "floatlab", BlockSize: "16k"},
		},
	}
	if err := Validate(ps); err == nil {
		t.Error("expected error for invalid block_size 16k")
	}
}

func TestValidate_InvalidFailoverMode(t *testing.T) {
	ps := &ParsedStack{
		Extension: StackExtension{
			PrimaryNode: "node-alpha",
			Storage:     StorageConfig{Pool: "floatlab"},
			Failover:    FailoverConfig{Mode: "instant"},
		},
	}
	if err := Validate(ps); err == nil {
		t.Error("expected error for invalid failover.mode")
	}
}

func TestValidate_Valid(t *testing.T) {
	ps := &ParsedStack{
		Extension: StackExtension{
			PrimaryNode: "node-alpha",
			Storage: StorageConfig{
				Pool:        "floatlab",
				BlockSize:   "32k",
				Compression: "lz4",
			},
			Failover: FailoverConfig{Mode: "manual"},
		},
	}
	if err := Validate(ps); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestDatasetPath(t *testing.T) {
	got := DatasetPath("floatlab", "my-app")
	want := "floatlab/stacks/my-app"
	if got != want {
		t.Errorf("DatasetPath = %q, want %q", got, want)
	}
}

func TestValidate_ServiceVolumeInvalidBlockSize(t *testing.T) {
	ps := &ParsedStack{
		Extension: StackExtension{
			PrimaryNode: "node-alpha",
			Storage:     StorageConfig{Pool: "floatlab"},
		},
		ServiceVolumes: map[string]map[string]VolumeExtension{
			"db": {
				"data": {BlockSize: "999k"},
			},
		},
	}
	if err := Validate(ps); err == nil {
		t.Error("expected error for invalid service volume block_size")
	}
}
