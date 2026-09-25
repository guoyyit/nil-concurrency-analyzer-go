package analysis

import (
	"go/ast"
	"go/token"
	"go/types"
)

// builtinResult models builtin function results.
func (e *env) builtinResult(name string, call *ast.CallExpr, idx int) nilState {
	switch name {
	case "new":
		return stateNonNil
	case "make":
		// make(chan), make(map), make(slice) all return usable values.
		return stateNonNil
	case "len", "cap", "copy", "delete", "panic", "recover",
		"print", "println", "close", "append":
		// Not reference-typed results relevant to nil derefs.
		return stateUnknown
	case "min", "max":
		return stateUnknown
	}
	return stateUnknown
}

// stubKey identifies a library function for hand-written summaries.
type stubKey struct{ pkgPath, name string }

// stubTable summarizes common standard-library constructors/accessors so the
// analyzer does not depend on analyzing the whole module graph.
//
// States describe the first/reference result only; error correlation for
// (T, error) pairs is handled separately below.
var stubNonNil = map[stubKey]bool{
	{"context", "Background"}:             true,
	{"context", "TODO"}:                   true,
	{"context", "WithValue"}:              true,
	{"context", "WithCancel"}:             true,
	{"context", "WithDeadline"}:           true,
	{"context", "WithTimeout"}:            true,
	{"sync", "Once"}:                      false,
	{"sync", "NewCond"}:                   true,
	{"sync/atomic", "NewInt32"}:           true,
	{"sync/atomic", "NewInt64"}:           true,
	{"sync/atomic", "NewUint32"}:          true,
	{"sync/atomic", "NewUint64"}:          true,
	{"sync/atomic", "NewUintptr"}:         true,
	{"sync/atomic", "NewBool"}:            true,
	{"sync/atomic", "NewPointer"}:         true,
	{"bytes", "NewBuffer"}:                true,
	{"bytes", "NewBufferString"}:          true,
	{"bytes", "NewReader"}:                true,
	{"strings", "NewReader"}:              true,
	{"strings", "NewReplacer"}:            true,
	{"regexp", "MustCompile"}:             true,
	{"regexp", "Compile"}:                 false, // (T, error)
	{"os", "OpenFile"}:                    false,
	{"os", "Create"}:                      false,
	{"os", "Open"}:                        false,
	{"io", "Pipe"}:                        false,
	{"net", "Listen"}:                     false,
	{"net", "Dial"}:                       false,
	{"net/http", "NewRequest"}:            false,
	{"net/http", "NewRequestWithContext"}: false,
	{"net/http", "Get"}:                   false,
	{"net/http", "Post"}:                  false,
	{"net/http", "PostForm"}:              false,
	{"net/url", "Parse"}:                  false,
	{"time", "ParseDuration"}:             false,
	{"time", "Parse"}:                     false,
	{"time", "Date"}:                      true,
	{"time", "Now"}:                       true,
	{"errors", "New"}:                     true,
	{"fmt", "Errorf"}:                     true,
	{"path/filepath", "Glob"}:             false,
}

// stubResult returns the modeled nilness of result idx of a library call.
// Multi-result (T, error) functions: on the success branch T is NonNil for
// constructors we know, on the error branch T is Nil. The refinement happens
// via errorReturns modeling in caller processing; here we return the
// conservative join used when err is ignored.
func stubResult(fn *types.Func, idx int) nilState {
	sig, ok := fn.Type().(*types.Signature)
	if !ok {
		return stateUnknown
	}
	pkgPath := ""
	if fn.Pkg() != nil {
		pkgPath = fn.Pkg().Path()
	}
	key := stubKey{pkgPath: pkgPath, name: fn.Name()}
	nResults := sig.Results().Len()
	// Error result itself.
	if idx == nResults-1 && nResults > 1 {
		if _, ok := sig.Results().At(idx).Type().(*types.Interface); ok {
			return stateUnknown
		}
	}
	nonNil, known := stubNonNil[key]
	if !known {
		// Heuristic: well-known constructor name prefixes return allocated
		// objects on success, but with error results can be nil; keep Unknown
		// to stay sound unless single return.
		if nResults == 1 && looksLikeAllocator(fn.Name()) {
			return stateNonNil
		}
		return stateUnknown
	}
	if nResults > 1 && idx == 0 {
		// err ignored: may be nil on the error path -> possible nil.
		if nonNil {
			return stateMayNil
		}
		return stateMayNil
	}
	if nonNil {
		return stateNonNil
	}
	return stateMayNil
}

// looksLikeAllocator applies the New* naming convention.
func looksLikeAllocator(name string) bool {
	return len(name) > 3 && name[:3] == "New"
}

// stubCallSuccess models the (T, error) success/failure split for a stub when
// the caller tests the error. Returns (successState, failureState).
func stubCallSplit(fn *types.Func, idx int) (nilState, nilState) {
	if fn == nil {
		return stateUnknown, stateUnknown
	}
	sig, _ := fn.Type().(*types.Signature)
	if sig == nil || sig.Results().Len() < 2 || idx >= sig.Results().Len()-1 {
		return stateUnknown, stateUnknown
	}
	pkgPath := ""
	if fn.Pkg() != nil {
		pkgPath = fn.Pkg().Path()
	}
	_, known := stubNonNil[stubKey{pkgPath: pkgPath, name: fn.Name()}]
	if !known {
		return stateUnknown, stateUnknown
	}
	// Go convention: a (T, error) constructor yields a usable T exactly when
	// err == nil; on failure T is nil.
	return stateNonNil, stateNil
}

var _ = token.Pos(0)
