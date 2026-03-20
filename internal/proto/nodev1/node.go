// Package nodev1 contains hand-written Go types mirroring the protobuf
// definitions in proto/node/v1/node.proto.  When protoc tooling is available
// this package should be replaced with generated code.
package nodev1

// ---------------------------------------------------------------------------
// Node registration
// ---------------------------------------------------------------------------

// RegisterRequest is sent by a node agent to join the cluster.
type RegisterRequest struct {
	JoinToken    string            `json:"join_token"`
	PublicKey    []byte            `json:"public_key"`
	Capabilities *NodeCapabilities `json:"capabilities,omitempty"`
}

// RegisterResponse is returned by the control plane after a successful join.
type RegisterResponse struct {
	NodeID         string     `json:"node_id"`
	TLSCertificate []byte     `json:"tls_certificate"`
	TLSKey         []byte     `json:"tls_key"`
	CACertificate  []byte     `json:"ca_certificate"`
	WireGuardCIDR  string     `json:"wireguard_cidr"`
	Peers          []PeerInfo `json:"peers"`
}

// ---------------------------------------------------------------------------
// Heartbeat
// ---------------------------------------------------------------------------

// HeartbeatRequest carries periodic status updates from a node.
type HeartbeatRequest struct {
	NodeID string      `json:"node_id"`
	Status *NodeStatus `json:"status,omitempty"`
}

// HeartbeatResponse carries commands from the control plane to a node.
type HeartbeatResponse struct {
	Commands []Command `json:"commands"`
}

// ---------------------------------------------------------------------------
// Node capabilities & status
// ---------------------------------------------------------------------------

// NodeCapabilities describes the hardware and software of a node.
type NodeCapabilities struct {
	CPUCores     int32  `json:"cpu_cores"`
	RAMBytes     int64  `json:"ram_bytes"`
	DiskBytes    int64  `json:"disk_bytes"`
	KVMAvailable bool   `json:"kvm_available"`
	ZFSAvailable bool   `json:"zfs_available"`
	OSVersion    string `json:"os_version"`
	AgentVersion string `json:"agent_version"`
}

// NodeStatus contains live resource-usage metrics from a node.
type NodeStatus struct {
	CPUUsage       float64 `json:"cpu_usage"`
	RAMUsed        int64   `json:"ram_used"`
	RAMTotal       int64   `json:"ram_total"`
	DiskUsed       int64   `json:"disk_used"`
	DiskTotal      int64   `json:"disk_total"`
	VMCount        int32   `json:"vm_count"`
	NetworkRxBytes int64   `json:"network_rx_bytes"`
	NetworkTxBytes int64   `json:"network_tx_bytes"`
}

// ---------------------------------------------------------------------------
// Commands (control plane → node)
// ---------------------------------------------------------------------------

// CommandType enumerates the kinds of command that can be dispatched.
type CommandType int

const (
	CommandTypeUnknown CommandType = iota
	CommandTypeCreateVM
	CommandTypeStopVM
	CommandTypeDestroyVM
	CommandTypeDrain
	CommandTypeUpdateConfig
)

// Command is a tagged-union style wrapper around the various command types.
// At most one field should be non-nil.
type Command struct {
	Type         CommandType          `json:"type"`
	CreateVM     *VMCreateCommand     `json:"create_vm,omitempty"`
	StopVM       *VMStopCommand       `json:"stop_vm,omitempty"`
	DestroyVM    *VMDestroyCommand    `json:"destroy_vm,omitempty"`
	Drain        *DrainCommand        `json:"drain,omitempty"`
	UpdateConfig *UpdateConfigCommand `json:"update_config,omitempty"`
}

// VMCreateCommand tells a node to create a new micro-VM.
type VMCreateCommand struct {
	VMID      string            `json:"vm_id"`
	UserID    string            `json:"user_id"`
	BaseImage string            `json:"base_image"`
	VCPUCount int32             `json:"vcpu_count"`
	MemBytes  int64             `json:"mem_bytes"`
	DiskBytes int64             `json:"disk_bytes"`
	Metadata  map[string]string `json:"metadata,omitempty"`
}

// VMStopCommand tells a node to gracefully stop a VM.
type VMStopCommand struct {
	VMID string `json:"vm_id"`
}

// VMDestroyCommand tells a node to destroy (force-kill) a VM.
type VMDestroyCommand struct {
	VMID string `json:"vm_id"`
}

// DrainCommand tells a node to drain all VMs before the given deadline.
type DrainCommand struct {
	DeadlineSeconds int64 `json:"deadline_seconds"`
}

// UpdateConfigCommand sends a configuration update to a node.
type UpdateConfigCommand struct {
	Config map[string]string `json:"config"`
}

// ---------------------------------------------------------------------------
// Peer info (WireGuard mesh)
// ---------------------------------------------------------------------------

// PeerInfo describes a mesh peer for WireGuard configuration.
type PeerInfo struct {
	NodeID             string `json:"node_id"`
	WireGuardPublicKey string `json:"wireguard_public_key"`
	WireGuardEndpoint  string `json:"wireguard_endpoint"`
	WireGuardCIDR      string `json:"wireguard_cidr"`
}
