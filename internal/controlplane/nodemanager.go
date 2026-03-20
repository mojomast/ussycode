// Package controlplane implements the server-side logic for managing nodes
// in the ussyverse server pool.
package controlplane

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	nodev1 "github.com/mojomast/ussycode/internal/proto/nodev1"
)

// NodeHealth represents the health state of a node.
type NodeHealth int

const (
	NodeHealthy NodeHealth = iota // receiving heartbeats normally
	NodeUnknown                   // no heartbeat for 30s
	NodeOffline                   // no heartbeat for 5min
	NodeDead                      // no heartbeat for 1hr — VMs will be rescheduled
)

func (h NodeHealth) String() string {
	switch h {
	case NodeHealthy:
		return "healthy"
	case NodeUnknown:
		return "unknown"
	case NodeOffline:
		return "offline"
	case NodeDead:
		return "dead"
	default:
		return "invalid"
	}
}

// Timeout thresholds for node health transitions.
const (
	TimeoutUnknown = 30 * time.Second
	TimeoutOffline = 5 * time.Minute
	TimeoutDead    = 1 * time.Hour
)

// TrackedNode holds all known state for a registered node.
type TrackedNode struct {
	ID           string
	Health       NodeHealth
	Capabilities *nodev1.NodeCapabilities
	LastStatus   *nodev1.NodeStatus
	LastSeen     time.Time
	RegisteredAt time.Time

	// pendingCommands are queued for the next heartbeat response.
	pendingCommands []nodev1.Command
}

// NodeManager tracks all registered nodes, processes heartbeats, and
// dispatches commands.
type NodeManager struct {
	mu     sync.RWMutex
	nodes  map[string]*TrackedNode
	logger *slog.Logger

	// onNodeDead is called when a node transitions to Dead state.
	// The callback receives the node ID and should trigger VM rescheduling.
	onNodeDead func(nodeID string)
}

// NewNodeManager creates a new NodeManager.
func NewNodeManager(opts ...NodeManagerOption) *NodeManager {
	nm := &NodeManager{
		nodes:  make(map[string]*TrackedNode),
		logger: slog.Default().With("component", "node-manager"),
	}
	for _, opt := range opts {
		opt(nm)
	}
	return nm
}

// NodeManagerOption configures a NodeManager.
type NodeManagerOption func(*NodeManager)

// WithOnNodeDead sets the callback invoked when a node transitions to Dead.
func WithOnNodeDead(fn func(nodeID string)) NodeManagerOption {
	return func(nm *NodeManager) {
		nm.onNodeDead = fn
	}
}

// RegisterNode adds a new node to the manager.
func (nm *NodeManager) RegisterNode(nodeID string, caps *nodev1.NodeCapabilities) {
	nm.mu.Lock()
	defer nm.mu.Unlock()

	now := time.Now().UTC()
	nm.nodes[nodeID] = &TrackedNode{
		ID:           nodeID,
		Health:       NodeHealthy,
		Capabilities: caps,
		LastSeen:     now,
		RegisteredAt: now,
	}
	nm.logger.Info("node registered", "node_id", nodeID)
}

// RemoveNode removes a node from tracking.
func (nm *NodeManager) RemoveNode(nodeID string) {
	nm.mu.Lock()
	defer nm.mu.Unlock()

	delete(nm.nodes, nodeID)
	nm.logger.Info("node removed", "node_id", nodeID)
}

// ProcessHeartbeat updates a node's status and returns any pending commands.
func (nm *NodeManager) ProcessHeartbeat(ctx context.Context, req *nodev1.HeartbeatRequest) (*nodev1.HeartbeatResponse, error) {
	nm.mu.Lock()
	defer nm.mu.Unlock()

	node, ok := nm.nodes[req.NodeID]
	if !ok {
		return nil, fmt.Errorf("unknown node: %s", req.NodeID)
	}

	now := time.Now().UTC()
	oldHealth := node.Health
	node.Health = NodeHealthy
	node.LastSeen = now
	node.LastStatus = req.Status

	if oldHealth != NodeHealthy {
		nm.logger.Info("node recovered",
			"node_id", req.NodeID,
			"previous_health", oldHealth.String(),
		)
	}

	// Drain pending commands.
	resp := &nodev1.HeartbeatResponse{
		Commands: node.pendingCommands,
	}
	node.pendingCommands = nil

	return resp, nil
}

// EnqueueCommand adds a command to a node's pending queue.
func (nm *NodeManager) EnqueueCommand(nodeID string, cmd nodev1.Command) error {
	nm.mu.Lock()
	defer nm.mu.Unlock()

	node, ok := nm.nodes[nodeID]
	if !ok {
		return fmt.Errorf("unknown node: %s", nodeID)
	}

	node.pendingCommands = append(node.pendingCommands, cmd)
	nm.logger.Info("command enqueued",
		"node_id", nodeID,
		"command_type", cmd.Type,
	)
	return nil
}

// GetNode returns a snapshot of a tracked node. Returns nil if not found.
func (nm *NodeManager) GetNode(nodeID string) *TrackedNode {
	nm.mu.RLock()
	defer nm.mu.RUnlock()

	node, ok := nm.nodes[nodeID]
	if !ok {
		return nil
	}

	// Return a shallow copy.
	cp := *node
	return &cp
}

// ListNodes returns snapshots of all tracked nodes.
func (nm *NodeManager) ListNodes() []*TrackedNode {
	nm.mu.RLock()
	defer nm.mu.RUnlock()

	result := make([]*TrackedNode, 0, len(nm.nodes))
	for _, node := range nm.nodes {
		cp := *node
		result = append(result, &cp)
	}
	return result
}

// ListHealthyNodes returns nodes in Healthy state.
func (nm *NodeManager) ListHealthyNodes() []*TrackedNode {
	nm.mu.RLock()
	defer nm.mu.RUnlock()

	result := make([]*TrackedNode, 0)
	for _, node := range nm.nodes {
		if node.Health == NodeHealthy {
			cp := *node
			result = append(result, &cp)
		}
	}
	return result
}

// CheckTimeouts scans all nodes and transitions them through health states
// based on how long since their last heartbeat. Should be called periodically
// (e.g., every 10s).
func (nm *NodeManager) CheckTimeouts() {
	nm.mu.Lock()
	defer nm.mu.Unlock()

	now := time.Now().UTC()

	for _, node := range nm.nodes {
		elapsed := now.Sub(node.LastSeen)
		oldHealth := node.Health

		switch {
		case elapsed >= TimeoutDead:
			node.Health = NodeDead
		case elapsed >= TimeoutOffline:
			node.Health = NodeOffline
		case elapsed >= TimeoutUnknown:
			node.Health = NodeUnknown
		default:
			// Still healthy — no change.
			continue
		}

		if node.Health != oldHealth {
			nm.logger.Warn("node health changed",
				"node_id", node.ID,
				"from", oldHealth.String(),
				"to", node.Health.String(),
				"last_seen", node.LastSeen.Format(time.RFC3339),
			)

			if node.Health == NodeDead && nm.onNodeDead != nil {
				// Call outside the lock to avoid deadlock.
				// We defer the actual call.
				nodeID := node.ID
				defer nm.callOnNodeDead(nodeID)
			}
		}
	}
}

// callOnNodeDead invokes the dead-node callback outside the lock.
func (nm *NodeManager) callOnNodeDead(nodeID string) {
	if nm.onNodeDead != nil {
		nm.onNodeDead(nodeID)
	}
}

// RunTimeoutChecker starts a goroutine that periodically checks for timed-out
// nodes. It stops when ctx is cancelled.
func (nm *NodeManager) RunTimeoutChecker(ctx context.Context, interval time.Duration) {
	if interval == 0 {
		interval = 10 * time.Second
	}

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				nm.CheckTimeouts()
			}
		}
	}()
}

// NodeCount returns the total number of tracked nodes.
func (nm *NodeManager) NodeCount() int {
	nm.mu.RLock()
	defer nm.mu.RUnlock()
	return len(nm.nodes)
}
