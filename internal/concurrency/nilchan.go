package concurrency

import (
	"go/ast"
	"go/token"

	"github.com/guoyyit/nil-concurrency-analyzer-go/internal/diag"
)

// ValKind 常量与 nilflow 的 ValKind 对齐（避免反向依赖具体类型）。
const (
	kindUnknown  = 0
	kindNil      = 1
	kindNonNil   = 2
	kindMaybeNil = 3
)

// checkNilChanSend 检查向空通道发送导致的永久阻塞。
//   - 确定 nil：高置信（definite）
//   - 可能 nil（分支合并）：可疑（possible）
func checkNilChanSend(res nilChanSource, fset *token.FileSet) []diag.Diagnostic {
	var out []diag.Diagnostic
	for pos, sd := range res.Sends() {
		kind, ok := res.SendChanKind(pos)
		if !ok {
			continue
		}
		if kind != kindNil && kind != kindMaybeNil {
			continue
		}
		f, ln, col := fileLineCol(fset, pos)
		conf := diag.ConfidenceHigh
		cert := diag.CertaintyDefinite
		msg := "send on nil channel blocks forever: channel is provably nil"
		if kind == kindMaybeNil {
			conf = diag.ConfidenceSuspicious
			cert = diag.CertaintyPossible
			msg = "possible send on nil channel: channel may be nil on some path"
		}
		_ = sd
		out = append(out, diag.Diagnostic{
			Category:   diag.CatNilChanSend,
			Position:   diag.Position{File: f, Line: ln, Column: col},
			Message:    msg,
			Confidence: conf,
			Certainty:  cert,
		})
	}
	return out
}

// nilChanSource 是 nilflow 结果在并发包侧的最小接口。
type nilChanSource interface {
	SendChanKind(pos token.Pos) (int, bool)
	Sends() map[token.Pos]*ast.SendStmt
}
