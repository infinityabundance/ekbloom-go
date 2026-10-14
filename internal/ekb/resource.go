package ekb

import "sync/atomic"

// DefaultMaxSingleAllocation is the default cap on any whole-object allocation
// driven by a declared length (1 GiB), mirroring the Rust ResourcePolicy.
const DefaultMaxSingleAllocation = uint64(1) << 30

var (
	policyMax    atomic.Uint64
	breachCount  atomic.Uint64
	policyInited atomic.Bool
)

func ensurePolicy() {
	if !policyInited.Load() {
		policyMax.CompareAndSwap(0, DefaultMaxSingleAllocation)
		policyInited.Store(true)
	}
}

// MaxSingleAllocation returns the current allocation cap.
func MaxSingleAllocation() uint64 {
	ensurePolicy()
	return policyMax.Load()
}

// SetMaxSingleAllocation sets the cap on any single whole-object allocation. A
// returned error is never produced for a positive value; the parameter is a
// policy, not a guarantee.
func SetMaxSingleAllocation(limit uint64) { policyMax.Store(limit) }

// ResetBreaches clears the resource-policy breach counter (used by tests).
func ResetBreaches() {
	ensurePolicy()
	breachCount.Store(0)
}

// Breaches returns the number of whole-object allocations the default policy has
// rejected (a monotonically increasing counter, never reset except explicitly).
func Breaches() uint64 { return breachCount.Load() }

// checkDefaultAllocation enforces the resource policy on a whole-object
// allocation of n bytes, incrementing the breach counter on rejection.
func checkDefaultAllocation(n uint64) error {
	limit := MaxSingleAllocation()
	if n > limit {
		breachCount.Add(1)
		return &Error{Kind: KindAllocationTooLarge, Requested: n, Limit: limit}
	}
	return nil
}
