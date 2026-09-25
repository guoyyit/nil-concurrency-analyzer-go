package analysis

import (
	"go/types"
)

// summary is the cross-function nilness contract of one local function.
// It is populated in nilanalysis.go.
type summary struct {
	// returns[i] describes result i on the function's normal (non-error)
	// exits, correlated with the error result when present.
	returns []nilState
	// successReturns describes companions on exits where the indicator
	// signals success (err==nil / bool==true).
	successReturns []nilState
	// errIndex is the index of the error-typed final result, or -1.
	errIndex int
	// errorReturns gives states of all results on exits where err != nil.
	errorReturns []nilState
	// boolIndex is the index of a boolean final result used as a success
	// indicator, or -1.
	boolIndex int
	// boolReturns gives states on exits where the boolean is false (failure).
	boolReturns []nilState
	// paramReturn[i] = j means result i is exactly parameter j on some exit
	// (identity/passthrough functions); -1 when not tracked.
	paramReturn []int
}

func newSummary(sig *types.Signature) *summary {
	n := sig.Results().Len()
	s := &summary{
		returns:        make([]nilState, n),
		successReturns: make([]nilState, n),
		errorReturns:   make([]nilState, n),
		boolReturns:    make([]nilState, n),
		errIndex:       -1,
		boolIndex:      -1,
	}
	for i := range s.returns {
		s.returns[i] = stateUnknown
		s.successReturns[i] = stateUnknown
		s.errorReturns[i] = stateUnknown
		s.boolReturns[i] = stateUnknown
	}
	if n > 0 {
		last := sig.Results().At(n - 1)
		if isErrorType(last.Type()) {
			s.errIndex = n - 1
		} else if b, ok := last.Type().(*types.Basic); ok && b.Kind() == types.Bool {
			s.boolIndex = n - 1
		}
	}
	return s
}
