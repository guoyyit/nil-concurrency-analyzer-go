package analysis

// nilState is the intraprocedural abstract value for nilness.
//
// The lattice (bottom -> top):
//
//	bottom(no path reaches)
//	   |
//	 Nil ---- NonNil
//	   \       /
//	    MayNil        (some reaching path nil, some non-nil)
//	      |
//	    Unknown       (no information; treated as "do not report")
//
// Unknown is deliberately above MayNil so that merging a known split with an
// unknown keeps the informative split, while a purely unknown value never
// produces a diagnostic.
type nilState int

const (
	stateBottom  nilState = iota // unreachable
	stateNil                     // nil on every reaching path
	stateNonNil                  // non-nil on every reaching path
	stateMayNil                  // nil on at least one path, non-nil on at least one
	stateUnknown                 // no information available
)

// merge is the lattice join.
func merge(a, b nilState) nilState {
	if a == stateBottom {
		return b
	}
	if b == stateBottom {
		return a
	}
	if a == b {
		return a
	}
	if a == stateUnknown || b == stateUnknown {
		// Unknown is the lattice top: no information subsumes any
		// informative state. Joining with it yields Unknown so that, e.g.,
		// "return nil" merged with "return m[k]" does not become a
		// (false) definite-nil.
		return stateUnknown
	}
	return stateMayNil
}

// mayBeNil reports whether a dereference in this state can see nil.
func (s nilState) mayBeNil() bool { return s == stateNil || s == stateMayNil }

// isNil reports whether the state is nil on all reaching paths.
func (s nilState) isNil() bool { return s == stateNil }

func (s nilState) String() string {
	switch s {
	case stateBottom:
		return "bottom"
	case stateNil:
		return "nil"
	case stateNonNil:
		return "nonnil"
	case stateMayNil:
		return "may-nil"
	default:
		return "unknown"
	}
}

// meetNil intersects a state known from data flow with a state contributed by
// branch refinement. When both are exact and agree we keep the exact state;
// when they disagree the value could be either on a may-reach path.
func meetNil(a, b nilState) nilState {
	// The branch condition is authoritative on its own path: an exact
	// refined state overrides whatever data flow knew before.
	switch b {
	case stateUnknown, stateBottom:
		return a
	default:
		return b
	}
}

// lockFlowState is the per-lock state tracked along a path.
type lockFlowState int

const (
	lockUnlocked lockFlowState = iota
	lockLocked
	lockMaybe // paths differ (one locked, one unlocked)
)

// mergeLock joins lock states at a control-flow join.
func mergeLock(a, b lockFlowState) lockFlowState {
	if a == b {
		return a
	}
	return lockMaybe
}
