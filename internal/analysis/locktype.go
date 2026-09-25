package analysis

import (
	"go/types"
)

// lockKind identifies a Go synchronization primitive stored by value.
type lockKind int

const (
	lockNone lockKind = iota
	lockMutex
	lockRWMutex
	lockWaitGroup
	lockOnce
	lockAtomic
	lockCond
)

// lockInfo describes a type that must never be copied after first use.
type lockInfo struct {
	kind lockKind
	name string
}

// containsLock reports whether t (after dereferencing) transitively contains
// a by-value synchronization primitive.
func containsLock(t types.Type) (lockInfo, bool) {
	return containsLockSeen(t, map[types.Type]bool{})
}

func containsLockSeen(t types.Type, seen map[types.Type]bool) (lockInfo, bool) {
	if t == nil || seen[t] {
		return lockInfo{}, false
	}
	seen[t] = true
	switch u := underlying(t).(type) {
	case *types.Struct:
		for i := 0; i < u.NumFields(); i++ {
			f := u.Field(i)
			if info, ok := lockPrimitive(f.Type()); ok {
				return info, true
			}
			// Recurse only into non-pointer fields: copying a struct copies
			// its value fields, but a *T field is just a pointer.
			if _, isPtr := underlying(f.Type()).(*types.Pointer); isPtr {
				continue
			}
			if info, ok := containsLockSeen(f.Type(), seen); ok {
				return info, true
			}
		}
	}
	return lockInfo{}, false
}

// lockPrimitive matches the concrete standard-library types directly.
func lockPrimitive(t types.Type) (lockInfo, bool) {
	n, ok := t.(*types.Named)
	if !ok {
		return lockInfo{}, false
	}
	obj := n.Obj()
	if obj == nil || obj.Pkg() == nil {
		return lockInfo{}, false
	}
	switch obj.Pkg().Path() + "." + obj.Name() {
	case "sync.Mutex":
		return lockInfo{kind: lockMutex, name: "sync.Mutex"}, true
	case "sync.RWMutex":
		return lockInfo{kind: lockRWMutex, name: "sync.RWMutex"}, true
	case "sync.WaitGroup":
		return lockInfo{kind: lockWaitGroup, name: "sync.WaitGroup"}, true
	case "sync.Once":
		return lockInfo{kind: lockOnce, name: "sync.Once"}, true
	case "sync.Cond":
		return lockInfo{kind: lockCond, name: "sync.Cond"}, true
	}
	// sync/atomic pointer-ish types carry noCopy by convention.
	if obj.Pkg().Path() == "sync/atomic" {
		switch obj.Name() {
		case "Int32", "Int64", "Uint32", "Uint64", "Uintptr", "Bool", "Pointer":
			return lockInfo{kind: lockAtomic, name: "sync/atomic." + obj.Name()}, true
		}
	}
	return lockInfo{}, false
}

// isLockMethod reports whether (recv-type, method) is one of the lock ops.
func lockMethodName(sel *types.Selection) (string, bool) {
	if sel == nil || sel.Kind() != types.MethodVal {
		return "", false
	}
	fn, ok := sel.Obj().(*types.Func)
	if !ok {
		return "", false
	}
	name := fn.Name()
	switch name {
	case "Lock", "Unlock", "RLock", "RUnlock", "Wait", "Add", "Done",
		"Signal", "Broadcast", "Do", "TryLock", "TryRLock":
		return name, true
	}
	return "", false
}
