// Package scheduler implements a two-phase VM placement scheduler for the
// ussyverse server pool. Phase 1 (Filter) removes nodes that fail hard
// constraints. Phase 2 (Score) ranks remaining nodes using weighted soft
// preferences.
package scheduler

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// Domain types
// ---------------------------------------------------------------------------

// VMSpec describes the resource requirements for a new VM.
type VMSpec struct {
	VMID      string
	UserID    string
	BaseImage string
	VCPUs     int32
	MemBytes  int64
	DiskBytes int64

	// Locality hints: prefer nodes where the user already has VMs.
	PreferNodeIDs []string

	// TrustLevel minimum required (0 = untrusted, 100 = fully trusted).
	MinTrustLevel int
}

// NodeStatus represents the current state of a node as seen by the scheduler.
type NodeStatus struct {
	ID         string
	Ready      bool
	Health     string // "healthy", "unknown", "offline", "dead"
	TrustLevel int

	// Capacity.
	CPUCores  int32
	RAMTotal  int64
	DiskTotal int64

	// Current usage.
	CPUUsed  float64 // 0.0–1.0
	RAMUsed  int64
	DiskUsed int64
	VMCount  int32

	// When this node joined (for freshness scoring).
	RegisteredAt time.Time

	// Which users have VMs on this node (for locality scoring).
	UserIDs []string

	// Draining means the node is being emptied and should not accept new VMs.
	Draining bool
}

// AvailableRAM returns RAM bytes available for new VMs.
func (n *NodeStatus) AvailableRAM() int64 {
	avail := n.RAMTotal - n.RAMUsed
	if avail < 0 {
		return 0
	}
	return avail
}

// AvailableDisk returns disk bytes available.
func (n *NodeStatus) AvailableDisk() int64 {
	avail := n.DiskTotal - n.DiskUsed
	if avail < 0 {
		return 0
	}
	return avail
}

// ---------------------------------------------------------------------------
// Scheduler interface
// ---------------------------------------------------------------------------

// Scheduler places VMs on nodes in the server pool.
type Scheduler interface {
	// PlaceVM selects the best node for the given VM spec.
	PlaceVM(ctx context.Context, spec VMSpec) (*NodeStatus, error)

	// DrainNode initiates draining of a node: no new VMs, existing VMs
	// will be rescheduled.
	DrainNode(ctx context.Context, nodeID string) error

	// ListNodes returns the status of all tracked nodes.
	ListNodes(ctx context.Context) ([]*NodeStatus, error)
}

// NodeProvider abstracts the source of node status information.
type NodeProvider interface {
	// GetNodes returns all nodes known to the system.
	GetNodes(ctx context.Context) ([]*NodeStatus, error)

	// MarkDraining marks a node as draining.
	MarkDraining(ctx context.Context, nodeID string) error
}

// ---------------------------------------------------------------------------
// Score weights
// ---------------------------------------------------------------------------

// ScoreWeights configures the relative importance of each scoring dimension.
type ScoreWeights struct {
	BinPacking float64 // prefer nodes that are already loaded (pack tightly)
	Spread     float64 // prefer spreading VMs across nodes
	Locality   float64 // prefer nodes already running user's VMs
	Freshness  float64 // prefer recently registered nodes
	Trust      float64 // prefer higher trust-level nodes
}

// DefaultWeights returns the default scoring weights.
func DefaultWeights() ScoreWeights {
	return ScoreWeights{
		BinPacking: 0.4,
		Spread:     0.2,
		Locality:   0.2,
		Freshness:  0.1,
		Trust:      0.1,
	}
}

// ---------------------------------------------------------------------------
// Default scheduler implementation
// ---------------------------------------------------------------------------

// DefaultScheduler is the standard two-phase scheduler.
type DefaultScheduler struct {
	mu       sync.RWMutex
	provider NodeProvider
	weights  ScoreWeights
	logger   *slog.Logger
}

// New creates a DefaultScheduler.
func New(provider NodeProvider, opts ...Option) *DefaultScheduler {
	s := &DefaultScheduler{
		provider: provider,
		weights:  DefaultWeights(),
		logger:   slog.Default().With("component", "scheduler"),
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Option configures the scheduler.
type Option func(*DefaultScheduler)

// WithWeights overrides the default scoring weights.
func WithWeights(w ScoreWeights) Option {
	return func(s *DefaultScheduler) {
		s.weights = w
	}
}

// PlaceVM runs the two-phase filter+score algorithm and returns the best node.
func (s *DefaultScheduler) PlaceVM(ctx context.Context, spec VMSpec) (*NodeStatus, error) {
	nodes, err := s.provider.GetNodes(ctx)
	if err != nil {
		return nil, fmt.Errorf("fetching nodes: %w", err)
	}

	// Phase 1: Filter.
	candidates := s.filter(nodes, spec)
	if len(candidates) == 0 {
		return nil, fmt.Errorf("no nodes passed filtering for VM %s (need %d MB RAM, %d vCPUs)",
			spec.VMID, spec.MemBytes/(1024*1024), spec.VCPUs)
	}

	s.logger.Info("filter phase complete",
		"vm_id", spec.VMID,
		"total_nodes", len(nodes),
		"candidates", len(candidates),
	)

	// Phase 2: Score.
	type scoredNode struct {
		node  *NodeStatus
		score float64
	}
	scored := make([]scoredNode, len(candidates))
	for i, node := range candidates {
		scored[i] = scoredNode{
			node:  node,
			score: s.score(node, spec, candidates),
		}
	}

	// Sort descending by score.
	sort.Slice(scored, func(i, j int) bool {
		return scored[i].score > scored[j].score
	})

	winner := scored[0]
	s.logger.Info("placement decision",
		"vm_id", spec.VMID,
		"node_id", winner.node.ID,
		"score", fmt.Sprintf("%.3f", winner.score),
	)

	return winner.node, nil
}

// DrainNode marks a node for draining via the provider.
func (s *DefaultScheduler) DrainNode(ctx context.Context, nodeID string) error {
	s.logger.Info("draining node", "node_id", nodeID)
	return s.provider.MarkDraining(ctx, nodeID)
}

// ListNodes returns all nodes from the provider.
func (s *DefaultScheduler) ListNodes(ctx context.Context) ([]*NodeStatus, error) {
	return s.provider.GetNodes(ctx)
}

// ---------------------------------------------------------------------------
// Phase 1: Filter
// ---------------------------------------------------------------------------

// filter removes nodes that fail hard constraints.
func (s *DefaultScheduler) filter(nodes []*NodeStatus, spec VMSpec) []*NodeStatus {
	var result []*NodeStatus
	for _, n := range nodes {
		if !n.Ready {
			continue
		}
		if n.Health != "healthy" {
			continue
		}
		if n.Draining {
			continue
		}
		if n.AvailableRAM() < spec.MemBytes {
			continue
		}
		if spec.DiskBytes > 0 && n.AvailableDisk() < spec.DiskBytes {
			continue
		}
		if spec.MinTrustLevel > 0 && n.TrustLevel < spec.MinTrustLevel {
			continue
		}
		result = append(result, n)
	}
	return result
}

// ---------------------------------------------------------------------------
// Phase 2: Score
// ---------------------------------------------------------------------------

// score computes a composite score for a node given the VM spec and the full
// candidate set (needed for spread scoring).
func (s *DefaultScheduler) score(node *NodeStatus, spec VMSpec, candidates []*NodeStatus) float64 {
	w := s.weights
	total := 0.0

	total += w.BinPacking * scoreBinPacking(node, spec)
	total += w.Spread * scoreSpread(node, candidates)
	total += w.Locality * scoreLocality(node, spec)
	total += w.Freshness * scoreFreshness(node)
	total += w.Trust * scoreTrust(node)

	return total
}

// scoreBinPacking prefers nodes that are already more loaded, packing VMs
// tightly to leave other nodes free.
func scoreBinPacking(node *NodeStatus, spec VMSpec) float64 {
	if node.RAMTotal == 0 {
		return 0
	}
	// After placing this VM, what fraction of RAM would be used?
	futureUsed := float64(node.RAMUsed+spec.MemBytes) / float64(node.RAMTotal)
	if futureUsed > 1.0 {
		futureUsed = 1.0
	}
	return futureUsed
}

// scoreSpread prefers nodes with fewer VMs (spread across the pool).
func scoreSpread(node *NodeStatus, candidates []*NodeStatus) float64 {
	if len(candidates) == 0 {
		return 0
	}

	// Find the max VM count across candidates.
	maxVMs := int32(1)
	for _, c := range candidates {
		if c.VMCount > maxVMs {
			maxVMs = c.VMCount
		}
	}

	// Higher score for fewer VMs.
	return 1.0 - (float64(node.VMCount) / float64(maxVMs))
}

// scoreLocality prefers nodes where the user already has VMs.
func scoreLocality(node *NodeStatus, spec VMSpec) float64 {
	// Check if the user already has VMs on this node.
	for _, uid := range node.UserIDs {
		if uid == spec.UserID {
			return 1.0
		}
	}

	// Check preferred node hints.
	for _, preferred := range spec.PreferNodeIDs {
		if node.ID == preferred {
			return 0.8
		}
	}

	return 0
}

// scoreFreshness slightly prefers recently registered nodes (newer hardware,
// updated software).
func scoreFreshness(node *NodeStatus) float64 {
	if node.RegisteredAt.IsZero() {
		return 0.5
	}
	age := time.Since(node.RegisteredAt)
	// Scale: 0 days = 1.0, 365 days = 0.0.
	days := age.Hours() / 24
	score := 1.0 - (days / 365.0)
	return math.Max(0, math.Min(1.0, score))
}

// scoreTrust prefers nodes with higher trust levels.
func scoreTrust(node *NodeStatus) float64 {
	return float64(node.TrustLevel) / 100.0
}

// Verify interface compliance.
var _ Scheduler = (*DefaultScheduler)(nil)
