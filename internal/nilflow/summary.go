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
	// SiblingCond 记录返回值之间的条件空值关联：
	// 当第 SibIdx 个返回值（error 或 bool）为真/为假时，第 i 个数据结果的空值。
	// 仅在被条件兄弟（error/ok 惯用法）切分后仍有意义时填充。
	SiblingCond []SiblingCond
	// DerefParams 记录函数体内无条件解引用的形参下标（接收者为 0）。
	DerefParams []int
	// DerefPos 给出各 DerefParams 解引用点的位置，用于调用路径展示。
	DerefPos map[int]token.Pos
	BodyHash string
}

// SiblingCond 描述“在第 SibIdx 个返回值取某个真值时，结果 ResultIdx 的空值”。
type SiblingCond struct {
	ResultIdx int
	SibIdx    int
	SibIsErr  bool // 兄弟返回值是否为 error（否则为 bool ok）
	WhenTrue  Val  // 兄弟为真（err != nil / ok == true）时数据的空值
	WhenFalse Val  // 兄弟为假（err == nil / ok == false）时数据的空值
	HasTrue   bool // 是否存在兄弟为真的可达返回路径
	HasFalse  bool // 是否存在兄弟为假的可达返回路径
	trueNil   bool // true 桶中是否出现过确定 nil
	falseNil  bool // false 桶中是否出现过确定 nil
}

// condLookup 在条件摘要表中查找 (resultIdx, sibIdx) 关联。
func condLookup(conds []SiblingCond, resultIdx, sibIdx int) *SiblingCond {
	for i := range conds {
		if conds[i].ResultIdx == resultIdx && conds[i].SibIdx == sibIdx {
			return &conds[i]
		}
	}
	return nil
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
