### Raft Implementation

This package provides a basic Raft implementation using `github.com/hashicorp/raft` to achieve distributed consensus within the FloatLab core.

#### Overview

The implementation facilitates leadership election and log replication across a cluster of nodes. In its current state, it is configured for a single-node bootstrap but can be extended for multi-node configurations.

#### Components

- **FSM (Finite State Machine)**: A simple FSM implementation that processes log entries. Currently, it prints the applied log data to standard output.
- **Snapshot**: A minimal snapshot implementation for FSM state persistence (currently using `DiscardSnapshotStore`).
- **NewRaftNode**: A helper function to initialize and bootstrap a Raft node. It sets up:
    - TCP Transport for inter-node communication.
    - In-memory log and stable storage.
    - Discard snapshot store.
    - A single-node cluster configuration.

#### Usage

To create a new Raft node:

```go
package main

import (
	"log"
	"time"
	"github.com/floatlab/floatlab-core/pkg/raft"
)

func main() {
	nodeID := "node1"
	address := "127.0.0.1:7000"

	r, err := raft.NewRaftNode(nodeID, address)
	if err != nil {
		log.Fatalf("Failed to create raft node: %v", err)
	}

	// Apply a log entry (must be leader)
	future := r.Apply([]byte("my log data"), 1*time.Second)
	if err := future.Error(); err != nil {
		log.Fatalf("Failed to apply log: %v", err)
	}
}
```

#### Testing

The package includes tests to verify node initialization and log application.

Run tests using:

```bash
go test ./pkg/raft/...
```
