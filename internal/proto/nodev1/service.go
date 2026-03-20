package nodev1

import "context"

// NodeServiceServer is the server-side interface for the node gRPC service.
// Implementations live in the control plane.
type NodeServiceServer interface {
	// Register handles a node join request. The agent sends its join token,
	// public key, and capabilities; the control plane returns credentials and
	// mesh peer information.
	Register(ctx context.Context, req *RegisterRequest) (*RegisterResponse, error)

	// Heartbeat is a bidirectional stream. The node sends periodic status
	// updates and receives commands from the control plane.
	Heartbeat(stream HeartbeatStream) error
}

// NodeServiceClient is the client-side interface used by the agent to
// communicate with the control plane.
type NodeServiceClient interface {
	// Register sends a join request to the control plane.
	Register(ctx context.Context, req *RegisterRequest) (*RegisterResponse, error)

	// Heartbeat opens a bidirectional stream with the control plane.
	Heartbeat(ctx context.Context) (HeartbeatStream, error)
}

// HeartbeatStream represents a bidirectional heartbeat stream. Both the
// client and the server use this interface to send and receive messages.
type HeartbeatStream interface {
	// Send sends a heartbeat request (node → control plane).
	Send(*HeartbeatRequest) error

	// Recv receives a heartbeat response (control plane → node).
	Recv() (*HeartbeatResponse, error)

	// Close terminates the stream gracefully.
	Close() error
}
