package nilflow

import (
	"go/ast"
	"go/token"
	"go/types"

	"github.com/guoyyit/nil-concurrency-analyzer-go/internal/loader"
)

// ComputeSendStates 对包内所有函数独立计算每个发送语句通道表达式的空值状态。
//
// 该分析只依赖单个函数自身的过程内数据流（通道变量的赋值/合并），不跨函数，
// 因此每轮都可在全新 FileSet 上廉价重算，无需跨轮缓存 token.Pos，
// 从而保证增量与冷启动结果一致。
func ComputeSendStates(pi *loader.PkgInfo) map[token.Pos]ValKind {
	out := map[token.Pos]ValKind{}
	for _, fi := range pi.Ordered {
		for _, decl := range fi.File.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			fn := fnOfDecl(fd, pi.TypeInfo)
			if fn == nil {
				continue
			}
			env := FuncEnv{
				Func:       fn,
				Decl:       fd,
				Fset:       pi.Fset,
				Info:       pi.TypeInfo,
				SendStates: out,
				SendsOnly:  true,
			}
			_, _, _ = AnalyzeSummary(env, func(*types.Func) *Summary { return nil })
		}
	}
	return out
}
