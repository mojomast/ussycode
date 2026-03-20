package scheduler

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Mock NodeProvider
// ---------------------------------------------------------------------------

type mockProvider struct {
	nodes   []*NodeStatus
	drained map[string]bool
}

func newMockProvider(nodes []*NodeStatus) *mockProvider {
	return &mockProvider{
		nodes:   nodes,
		drained: make(map[string]bool),
	}
}

func (m *mockProvider) GetNodes(_ context.Context) ([]*NodeStatus, error) {
	// Apply draining state.
	result := make([]*NodeStatus, len(m.nodes))
	for i, n := range m.nodes {
		cp := *n
		if m.drained[n.ID] {
			cp.Draining = true
		}
		result[i] = &cp
	}
	return result, nil
}

func (m *mockProvider) MarkDraining(_ context.Context, nodeID string) error {
	m.drained[nodeID] = true
	return nil
}

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

func makeNode(id string, ramTotal, ramUsed int64, vmCount int32, trust int) *NodeStatus {
	return &NodeStatus{
		ID:           id,
		Ready:        true,
		Health:       "healthy",
		TrustLevel:   trust,
		CPUCores:     4,
		RAMTotal:     ramTotal,
		RAMUsed:      ramUsed,
		DiskTotal:    100 * 1024 * 1024 * 1024, // 100 GB
		DiskUsed:     10 * 1024 * 1024 * 1024,  // 10 GB
		VMCount:      vmCount,
		RegisteredAt: time.Now().Add(-24 * time.Hour),
	}
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestPlaceVM_BasicPlacement(t *testing.T) {
	nodes := []*NodeStatus{
		makeNode("node-1", 8*gb, 2*gb, 2, 80),
		makeNode("node-2", 8*gb, 4*gb, 4, 80),
		makeNode("node-3", 8*gb, 1*gb, 1, 80),
	}

	provider := newMockProvider(nodes)
	sched := New(provider)

	spec := VMSpec{
		VMID:     "vm-1",
		UserID:   "user-1",
		MemBytes: 1 * gb,
	}

	node, err := sched.PlaceVM(context.Background(), spec)
	if err != nil {
		t.Fatalf("PlaceVM: %v", err)
	}
	if node == nil {
		t.Fatal("PlaceVM returned nil node")
	}

	t.Logf("placed on %s (RAM used: %d/%d, VMs: %d)",
		node.ID, node.RAMUsed, node.RAMTotal, node.VMCount)
}

func TestPlaceVM_NoSuitableNodes(t *testing.T) {
	nodes := []*NodeStatus{
		makeNode("node-1", 1*gb, 900*mb, 5, 80),
		makeNode("node-2", 1*gb, 950*mb, 5, 80),
	}

	provider := newMockProvider(nodes)
	sched := New(provider)

	spec := VMSpec{
		VMID:     "vm-big",
		UserID:   "user-1",
		MemBytes: 2 * gb, // more than any node has available
	}

	_, err := sched.PlaceVM(context.Background(), spec)
	if err == nil {
		t.Fatal("expected error for insufficient resources")
	}
	t.Logf("got expected error: %v", err)
}

func TestPlaceVM_RespectsMinTrustLevel(t *testing.T) {
	nodes := []*NodeStatus{
		makeNode("low-trust", 8*gb, 1*gb, 1, 30),
		makeNode("high-trust", 8*gb, 1*gb, 1, 90),
	}

	provider := newMockProvider(nodes)
	sched := New(provider)

	spec := VMSpec{
		VMID:          "vm-secure",
		UserID:        "user-1",
		MemBytes:      1 * gb,
		MinTrustLevel: 50,
	}

	node, err := sched.PlaceVM(context.Background(), spec)
	if err != nil {
		t.Fatalf("PlaceVM: %v", err)
	}
	if node.ID != "high-trust" {
		t.Errorf("expected high-trust node, got %s", node.ID)
	}
}

func TestPlaceVM_SkipsDrainingNodes(t *testing.T) {
	nodes := []*NodeStatus{
		makeNode("draining", 8*gb, 1*gb, 1, 80),
		makeNode("active", 8*gb, 3*gb, 3, 80),
	}
	nodes[0].Draining = true

	provider := newMockProvider(nodes)
	sched := New(provider)

	spec := VMSpec{
		VMID:     "vm-1",
		UserID:   "user-1",
		MemBytes: 1 * gb,
	}

	node, err := sched.PlaceVM(context.Background(), spec)
	if err != nil {
		t.Fatalf("PlaceVM: %v", err)
	}
	if node.ID != "active" {
		t.Errorf("expected active node, got %s", node.ID)
	}
}

func TestPlaceVM_SkipsUnhealthyNodes(t *testing.T) {
	nodes := []*NodeStatus{
		makeNode("unhealthy", 8*gb, 1*gb, 1, 80),
		makeNode("healthy", 8*gb, 3*gb, 3, 80),
	}
	nodes[0].Health = "offline"

	provider := newMockProvider(nodes)
	sched := New(provider)

	spec := VMSpec{
		VMID:     "vm-1",
		UserID:   "user-1",
		MemBytes: 1 * gb,
	}

	node, err := sched.PlaceVM(context.Background(), spec)
	if err != nil {
		t.Fatalf("PlaceVM: %v", err)
	}
	if node.ID != "healthy" {
		t.Errorf("expected healthy node, got %s", node.ID)
	}
}

func TestPlaceVM_LocalityPreference(t *testing.T) {
	nodes := []*NodeStatus{
		makeNode("node-empty", 8*gb, 1*gb, 1, 80),
		makeNode("node-user", 8*gb, 4*gb, 4, 80),
	}
	// node-user already has VMs for the requesting user.
	nodes[1].UserIDs = []string{"user-1"}

	provider := newMockProvider(nodes)
	// Use heavy locality weight.
	sched := New(provider, WithWeights(ScoreWeights{
		BinPacking: 0.1,
		Spread:     0.1,
		Locality:   0.6,
		Freshness:  0.1,
		Trust:      0.1,
	}))

	spec := VMSpec{
		VMID:     "vm-1",
		UserID:   "user-1",
		MemBytes: 1 * gb,
	}

	node, err := sched.PlaceVM(context.Background(), spec)
	if err != nil {
		t.Fatalf("PlaceVM: %v", err)
	}
	if node.ID != "node-user" {
		t.Errorf("expected node-user (locality), got %s", node.ID)
	}
}

func TestPlaceVM_BinPackingPreference(t *testing.T) {
	// With strong bin-packing weight, scheduler should prefer the
	// already-loaded node to pack VMs tightly.
	nodes := []*NodeStatus{
		makeNode("mostly-empty", 8*gb, 1*gb, 1, 80),
		makeNode("mostly-full", 8*gb, 6*gb, 6, 80),
	}

	provider := newMockProvider(nodes)
	sched := New(provider, WithWeights(ScoreWeights{
		BinPacking: 0.9,
		Spread:     0.0,
		Locality:   0.0,
		Freshness:  0.0,
		Trust:      0.1,
	}))

	spec := VMSpec{
		VMID:     "vm-1",
		UserID:   "user-1",
		MemBytes: 1 * gb,
	}

	node, err := sched.PlaceVM(context.Background(), spec)
	if err != nil {
		t.Fatalf("PlaceVM: %v", err)
	}
	if node.ID != "mostly-full" {
		t.Errorf("expected mostly-full (bin-packing), got %s", node.ID)
	}
}

func TestDrainNode(t *testing.T) {
	nodes := []*NodeStatus{
		makeNode("node-1", 8*gb, 1*gb, 1, 80),
		makeNode("node-2", 8*gb, 1*gb, 1, 80),
	}

	provider := newMockProvider(nodes)
	sched := New(provider)

	err := sched.DrainNode(context.Background(), "node-1")
	if err != nil {
		t.Fatalf("DrainNode: %v", err)
	}

	// Now placement should skip node-1.
	spec := VMSpec{
		VMID:     "vm-after-drain",
		UserID:   "user-1",
		MemBytes: 1 * gb,
	}
	node, err := sched.PlaceVM(context.Background(), spec)
	if err != nil {
		t.Fatalf("PlaceVM after drain: %v", err)
	}
	if node.ID != "node-2" {
		t.Errorf("expected node-2 after draining node-1, got %s", node.ID)
	}
}

func TestListNodes(t *testing.T) {
	nodes := []*NodeStatus{
		makeNode("node-1", 8*gb, 1*gb, 1, 80),
		makeNode("node-2", 8*gb, 2*gb, 2, 80),
		makeNode("node-3", 8*gb, 3*gb, 3, 80),
	}

	provider := newMockProvider(nodes)
	sched := New(provider)

	result, err := sched.ListNodes(context.Background())
	if err != nil {
		t.Fatalf("ListNodes: %v", err)
	}
	if len(result) != 3 {
		t.Errorf("expected 3 nodes, got %d", len(result))
	}
}

func TestPlaceVM_ManyNodes(t *testing.T) {
	// Stress test with many nodes.
	nodes := make([]*NodeStatus, 100)
	for i := range nodes {
		nodes[i] = makeNode(
			fmt.Sprintf("node-%03d", i),
			16*gb,
			int64(i)*100*mb,
			int32(i),
			50+i%50,
		)
	}

	provider := newMockProvider(nodes)
	sched := New(provider)

	spec := VMSpec{
		VMID:     "vm-stress",
		UserID:   "user-1",
		MemBytes: 1 * gb,
	}

	node, err := sched.PlaceVM(context.Background(), spec)
	if err != nil {
		t.Fatalf("PlaceVM: %v", err)
	}
	if node == nil {
		t.Fatal("PlaceVM returned nil")
	}
	t.Logf("placed on %s", node.ID)
}

// Size constants.
const (
	mb = 1024 * 1024
	gb = 1024 * 1024 * 1024
)
