package analysis

import "go/types"

// underlying dereferences named types to their underlying kind.
func underlying(t types.Type) types.Type {
	for {
		n, ok := t.(*types.Named)
		if !ok {
			return t
		}
		t = n.Underlying()
	}
}

// isPointerish reports whether values of t can be nil and are dereferenceable
// (pointer, interface, map, chan, slice, func).
func isPointerish(t types.Type) bool {
	switch underlying(t).(type) {
	case *types.Pointer, *types.Interface, *types.Map, *types.Chan,
		*types.Slice, *types.Signature:
		return true
	}
	return false
}

// derefType reports whether a dereference of a value of t is a nil-pointer
// panic risk (pointers and interfaces).
func derefType(t types.Type) bool {
	switch underlying(t).(type) {
	case *types.Pointer, *types.Interface:
		return true
	}
	return false
}
