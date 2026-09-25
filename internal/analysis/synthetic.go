package analysis

import (
	"fmt"
	"go/types"
)

// resultVar is a synthetic variable standing for one result value of a call
// or an ok-pattern extraction, used as an env map key.
type resultVar struct {
	types.Var
	kind string
	pos  string
	idx  int
}

func newResultVar(kind, pos string, idx int, t types.Type) *types.Var {
	v := types.NewVar(0, nil, fmt.Sprintf("%s$%s$%d", kind, pos, idx), t)
	return v
}
