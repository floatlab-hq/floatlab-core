package ipc

// WorkloadAddressProbe checks whether a remote MAC answers for a workload IP.
type WorkloadAddressProbe struct {
	Address       string `json:"address"`
	Interface     string `json:"interface,omitempty"`
	DockerNetwork string `json:"docker_network,omitempty"`
}
