package analysis

import (
	"go/ast"
	"go/types"
)

// indicatorKind tells how a call's last result expresses failure.
type indicatorKind int

const (
	indError indicatorKind = iota // nil = success
	indBool                       // true = success, false = failure
)

// rel links an indicator result variable (err/ok) to a companion value result
// so branch tests can refine the companion's nilness.
type rel struct {
	val       *types.Var
	call      *ast.CallExpr
	idx       int
	indicator int
	kind      indicatorKind
}
