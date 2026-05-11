package compose

import "fmt"

var validBlockSizes = map[string]bool{
	"4k": true, "8k": true, "32k": true, "128k": true,
}

var validCompression = map[string]bool{
	"none": true, "lz4": true, "gzip": true, "zstd": true,
}

var validFailoverModes = map[string]bool{
	"manual": true, "auto": true,
}

// Validate checks required fields and enum values in a ParsedStack.
func Validate(ps *ParsedStack) error {
	ext := ps.Extension
	if ext.PrimaryNode == "" {
		return fmt.Errorf("compose: x-fl-stack.primary_node is required")
	}
	if ext.Storage.Pool == "" {
		return fmt.Errorf("compose: x-fl-stack.storage.pool is required")
	}
	if ext.Storage.BlockSize != "" && !validBlockSizes[ext.Storage.BlockSize] {
		return fmt.Errorf("compose: invalid block_size %q (must be 4k, 8k, 32k, or 128k)", ext.Storage.BlockSize)
	}
	if ext.Storage.Compression != "" && !validCompression[ext.Storage.Compression] {
		return fmt.Errorf("compose: invalid compression %q (must be none, lz4, gzip, or zstd)", ext.Storage.Compression)
	}
	if ext.Failover.Mode != "" && !validFailoverModes[ext.Failover.Mode] {
		return fmt.Errorf("compose: invalid failover.mode %q (must be manual or auto)", ext.Failover.Mode)
	}
	for svc, vols := range ps.ServiceVolumes {
		for vol, ve := range vols {
			if ve.BlockSize != "" && !validBlockSizes[ve.BlockSize] {
				return fmt.Errorf("compose: service %s volume %s: invalid block_size %q", svc, vol, ve.BlockSize)
			}
			if ve.Compression != "" && !validCompression[ve.Compression] {
				return fmt.Errorf("compose: service %s volume %s: invalid compression %q", svc, vol, ve.Compression)
			}
		}
	}
	return nil
}

// DatasetPath returns the canonical ZFS dataset path for a stack.
func DatasetPath(pool, stackName string) string {
	return pool + "/stacks/" + stackName
}
