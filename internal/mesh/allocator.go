package mesh

import (
	"encoding/binary"
	"fmt"
	"log/slog"
	"net"
	"sync"
)

// Allocator manages IP subnet allocation from the 100.64.0.0/10 Carrier-Grade
// NAT range. Each node gets a /24 subnet. The allocator tracks which subnets
// are in use and which are free.
//
// The range 100.64.0.0/10 spans 100.64.0.0 – 100.127.255.255, giving us
// 64 × 256 = 16384 possible /24 subnets. That's plenty for a server pool.
type Allocator struct {
	mu sync.Mutex

	// baseNet is the supernet from which subnets are carved.
	baseNet *net.IPNet

	// subnetBits is the prefix length of each allocation (default /24).
	subnetBits int

	// allocated maps node IDs to their allocated subnet.
	allocated map[string]*net.IPNet

	// used tracks which subnet indices are taken.
	used map[uint32]string // subnet index → node ID

	logger *slog.Logger
}

// NewAllocator creates a subnet allocator for the 100.64.0.0/10 range,
// assigning /24 subnets to each node.
func NewAllocator() *Allocator {
	_, baseNet, _ := net.ParseCIDR("100.64.0.0/10")
	return &Allocator{
		baseNet:    baseNet,
		subnetBits: 24,
		allocated:  make(map[string]*net.IPNet),
		used:       make(map[uint32]string),
		logger:     slog.Default().With("component", "ip-allocator"),
	}
}

// NewAllocatorCustom creates a subnet allocator with a custom base range and
// subnet size. Useful for testing.
func NewAllocatorCustom(baseCIDR string, subnetBits int) (*Allocator, error) {
	_, baseNet, err := net.ParseCIDR(baseCIDR)
	if err != nil {
		return nil, fmt.Errorf("parsing base CIDR: %w", err)
	}

	baseBits, _ := baseNet.Mask.Size()
	if subnetBits <= baseBits {
		return nil, fmt.Errorf("subnet bits (%d) must be greater than base bits (%d)", subnetBits, baseBits)
	}

	return &Allocator{
		baseNet:    baseNet,
		subnetBits: subnetBits,
		allocated:  make(map[string]*net.IPNet),
		used:       make(map[uint32]string),
		logger:     slog.Default().With("component", "ip-allocator"),
	}, nil
}

// Allocate assigns a /24 subnet to the given node. If the node already has
// an allocation, it returns the existing one.
func (a *Allocator) Allocate(nodeID string) (*net.IPNet, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	// Return existing allocation if any.
	if subnet, ok := a.allocated[nodeID]; ok {
		return subnet, nil
	}

	// Find the next free subnet index.
	maxIndex := a.maxSubnetIndex()
	for idx := uint32(0); idx < maxIndex; idx++ {
		if _, taken := a.used[idx]; !taken {
			subnet := a.subnetForIndex(idx)
			a.allocated[nodeID] = subnet
			a.used[idx] = nodeID
			a.logger.Info("allocated subnet",
				"node_id", nodeID,
				"subnet", subnet.String(),
				"index", idx,
			)
			return subnet, nil
		}
	}

	return nil, fmt.Errorf("no free subnets available (all %d exhausted)", maxIndex)
}

// Release frees the subnet allocated to the given node.
func (a *Allocator) Release(nodeID string) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	subnet, ok := a.allocated[nodeID]
	if !ok {
		return fmt.Errorf("no allocation for node %s", nodeID)
	}

	// Find and remove from the used map.
	for idx, nid := range a.used {
		if nid == nodeID {
			delete(a.used, idx)
			break
		}
	}
	delete(a.allocated, nodeID)

	a.logger.Info("released subnet",
		"node_id", nodeID,
		"subnet", subnet.String(),
	)
	return nil
}

// GetAllocation returns the subnet assigned to a node, or nil if none.
func (a *Allocator) GetAllocation(nodeID string) *net.IPNet {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.allocated[nodeID]
}

// ListAllocations returns a map of all current allocations (node ID → CIDR).
func (a *Allocator) ListAllocations() map[string]string {
	a.mu.Lock()
	defer a.mu.Unlock()

	result := make(map[string]string, len(a.allocated))
	for nodeID, subnet := range a.allocated {
		result[nodeID] = subnet.String()
	}
	return result
}

// FreeCount returns the number of available subnets.
func (a *Allocator) FreeCount() uint32 {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.maxSubnetIndex() - uint32(len(a.used))
}

// AllocatedCount returns the number of allocated subnets.
func (a *Allocator) AllocatedCount() uint32 {
	a.mu.Lock()
	defer a.mu.Unlock()
	return uint32(len(a.used))
}

// ---------------------------------------------------------------------------
// Persistence support (for DB-backed allocator)
// ---------------------------------------------------------------------------

// AllocationRecord is a serializable record for persisting allocations.
type AllocationRecord struct {
	NodeID string `json:"node_id"`
	CIDR   string `json:"cidr"`
}

// ExportAllocations returns all allocations as serializable records.
func (a *Allocator) ExportAllocations() []AllocationRecord {
	a.mu.Lock()
	defer a.mu.Unlock()

	records := make([]AllocationRecord, 0, len(a.allocated))
	for nodeID, subnet := range a.allocated {
		records = append(records, AllocationRecord{
			NodeID: nodeID,
			CIDR:   subnet.String(),
		})
	}
	return records
}

// ImportAllocations loads allocations from serialized records, typically
// read from a database on startup.
func (a *Allocator) ImportAllocations(records []AllocationRecord) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	for _, rec := range records {
		_, subnet, err := net.ParseCIDR(rec.CIDR)
		if err != nil {
			return fmt.Errorf("parsing CIDR %q for node %s: %w", rec.CIDR, rec.NodeID, err)
		}

		idx := a.indexForSubnet(subnet)
		a.allocated[rec.NodeID] = subnet
		a.used[idx] = rec.NodeID
	}

	a.logger.Info("imported allocations", "count", len(records))
	return nil
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

// maxSubnetIndex returns the total number of possible subnets.
func (a *Allocator) maxSubnetIndex() uint32 {
	baseBits, _ := a.baseNet.Mask.Size()
	// Number of subnets = 2^(subnetBits - baseBits)
	diff := a.subnetBits - baseBits
	return 1 << uint(diff)
}

// subnetForIndex computes the /subnetBits subnet for a given index.
func (a *Allocator) subnetForIndex(idx uint32) *net.IPNet {
	baseIP := ipToUint32(a.baseNet.IP.To4())

	// Each subnet is 2^(32 - subnetBits) addresses wide.
	subnetSize := uint32(1) << uint(32-a.subnetBits)
	subnetIP := baseIP + idx*subnetSize

	ip := uint32ToIP(subnetIP)
	mask := net.CIDRMask(a.subnetBits, 32)

	return &net.IPNet{IP: ip, Mask: mask}
}

// indexForSubnet computes the index of a given subnet within the base range.
func (a *Allocator) indexForSubnet(subnet *net.IPNet) uint32 {
	baseIP := ipToUint32(a.baseNet.IP.To4())
	subnetIP := ipToUint32(subnet.IP.To4())
	subnetSize := uint32(1) << uint(32-a.subnetBits)

	if subnetSize == 0 {
		return 0
	}
	return (subnetIP - baseIP) / subnetSize
}

func ipToUint32(ip net.IP) uint32 {
	ip = ip.To4()
	if ip == nil {
		return 0
	}
	return binary.BigEndian.Uint32(ip)
}

func uint32ToIP(n uint32) net.IP {
	ip := make(net.IP, 4)
	binary.BigEndian.PutUint32(ip, n)
	return ip
}
