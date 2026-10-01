// Package nilflow 实现空值的过程内与跨函数传播分析。
//
// 精度模型概览：
//   - 过程内为流敏感分析，按 CFG 风格的分支/合并跟踪每个标识符的空值状态；
//   - 过程间通过函数摘要（Summary）传播：每个返回值被抽象为 nil/非nil/可能nil；
//   - error/ok 失败模式在调用点通过“兄弟返回值条件精化”处理（见 branch.go）；
//   - 无法确定时按未知处理，避免对未知解引用产生误报（宁可漏报、不可滥报）。
package nilflow

import (
	"go/ast"
	"go/token"
	"go/types"

	"github.com/guoyyit/nil-concurrency-analyzer-go/internal/diag"
)

// Summary 描述一个函数对空值的影响。
type Summary struct {
	Func    *types.Func
	Results []Val // 每个返回值的空值抽象
	// ResultPos 记录每个返回值被判定为 nil/非nil 的代表性 return 位置。
	ResultPos []token.Pos
	// Paths 记录每条可达 return 语句上各结果值的元组，
	// 供调用点按 error/ok 兄弟返回值做路径相关精化。
	// Results 是 Paths 在全部路径上的保守合并。
	Paths []ReturnPath
	// DerefParams 记录函数体内无条件解引用的形参下标（接收者为 0）。
	DerefParams []int
	// DerefPos 给出各 DerefParams 解引用点的位置，用于调用路径展示。
	DerefPos map[int]token.Pos
	BodyHash string
}

// Truth 描述一个布尔返回值在某条 return 路径上的真假性。
type Truth int

const (
	TruthUnknown Truth = iota
	TruthTrue
	TruthFalse
)

// ReturnPath 是一条 return 语句的抽象：
// Vals 为各结果的空值格元素（此时 Call 为 nil，调用点会重新关联出处）；
// Truths 记录布尔结果的真假性，非布尔结果对应 TruthUnknown。
type ReturnPath struct {
	Vals   []Val
	Truths []Truth
	Pos    token.Pos
}

// FuncEnv 是分析单个函数时所需的环境。
type FuncEnv struct {
	Func         *types.Func
	Decl         *ast.FuncDecl
	Fset         *token.FileSet
	Info         *types.Info
	Caller       *Caller                       // 非根分析时的调用点（用于调用路径）
	SendStates   map[token.Pos]ValKind         // 共享：记录每个 send 语句通道表达式的空值
	ShowExcluded bool                          // 是否记录被保护分支排除的证据
	SendsOnly    bool                          // 轻量模式：只收集发送语句通道空值，不报解引用
	Decls        map[*types.Func]*ast.FuncDecl // 同包函数声明表（内联用）
}

// Resolver 返回被调函数的当前摘要；递归活动边或尚未计算时返回 nil。
type Resolver func(*types.Func) *Summary

// AnalyzeSummary 对单个函数做过程内分析，产出摘要、诊断以及与 FileSet 无关的 loc 诊断。
func AnalyzeSummary(env FuncEnv, resolve Resolver) (*Summary, []diag.Diagnostic, []locDiag) {
	a := &intraAnalyzer{env: env, resolve: resolve, declByFn: env.Decls}
	sum, ds := a.run()
	return sum, ds, a.ldiags
}
