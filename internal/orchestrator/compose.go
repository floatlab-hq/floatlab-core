package orchestrator

import (
	"github.com/floatlab/floatlab-core/pkg/compose"
	"github.com/floatlab/floatlab-core/pkg/config"
	"github.com/floatlab/floatlab-core/pkg/ipc"
)

func buildDatasetCreate(stackName string, ext compose.StackExtension) ipc.DatasetCreatePayload {
	blockSize := ext.Storage.BlockSize
	if blockSize == "" {
		blockSize = "32k"
	}
	compression := ext.Storage.Compression
	if compression == "" {
		compression = "lz4"
	}
	return ipc.DatasetCreatePayload{
		Dataset:     compose.DatasetPath(ext.Storage.Pool, stackName),
		BlockSize:   blockSize,
		Compression: compression,
		Quota:       ext.Storage.Quota,
	}
}

func buildComposeUp(stack *config.Stack) ipc.ComposeUpPayload {
	return ipc.ComposeUpPayload{
		StackID:     stack.ID,
		DatasetPath: stack.ZFSDataset,
		ComposeFile: stack.ComposeYAML,
	}
}

func buildComposeDown(stack *config.Stack) ipc.ComposeDownPayload {
	return ipc.ComposeDownPayload{
		StackID:       stack.ID,
		DatasetPath:   stack.ZFSDataset,
		RemoveVolumes: false,
	}
}
