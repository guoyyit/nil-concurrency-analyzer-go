package nilflow

import (
	"go/ast"
	"go/token"
	"go/types"

	"github.com/guoyyit/nil-concurrency-analyzer-go/internal/diag"
	"github.com/guoyyit/nil-concurrency-analyzer-go/internal/loader"
)

// PackageResult 是一个包的空值分析结果。
type PackageResult struct {
	PkgPath     string
	Diagnostics []diag.Diagnostic
	Summaries   map[string]*Summary
	// SendStates 记录每个发送语句通道表达式的空值（供 nil-channel-send 检查）。
	SendStates map[token.Pos]ValKind
	// Sends 记录发送语句节点，供并发检查定位。
	Sends map[token.Pos]*ast.SendStmt
}

// Analyzer 对一个包做跨函数空值分析，支持函数级增量缓存。
//
// 缓存以函数的稳定标识（全限定名）为键——go/types 在重新类型检查后会产生
// 全新的 *types.Func 指针，因此不能以指针作为跨分析轮次的缓存键。
type Analyzer struct {
	showExcluded bool
	cache        map[string]*funcCache
}

type funcCache struct {
	hash    string // 依赖闭包哈希
	summary *Summary
	diags   []locDiag // 与 FileSet 无关，跨轮复用安全
	diagFn  string    // 产生这些诊断的函数全名（主位置锚点）
}

func NewAnalyzer() *Analyzer {
	return &Analyzer{cache: map[string]*funcCache{}}
}

// SetShowExcluded 控制是否输出被保护分支排除的证据。
func (a *Analyzer) SetShowExcluded(v bool) { a.showExcluded = v }

type fnEntry struct {
	key  string
	fn   *types.Func
	decl *ast.FuncDecl
}

// funcKey 返回函数在包内的稳定标识：接收者类型名 + 函数名（不含签名位置）。
func funcKey(fn *types.Func) string { return fn.FullName() }

// Analyze 对给定包执行固定点分析。dirty 非空时仅重算这些键对应的函数，
// 其余函数复用缓存；返回的诊断与冷启动全量分析完全一致。
func (a *Analyzer) Analyze(pi *loader.PkgInfo, dirty map[string]bool) *PackageResult {
	res := &PackageResult{
		PkgPath:    pi.PkgPath,
		Summaries:  map[string]*Summary{},
		SendStates: map[token.Pos]ValKind{},
		Sends:      map[token.Pos]*ast.SendStmt{},
	}

	entries := collectFunctions(pi)
	byKey := map[string]*fnEntry{}
	declByFn := map[*types.Func]*ast.FuncDecl{}
	for _, e := range entries {
		byKey[e.key] = e
		declByFn[e.fn] = e.decl
	}

	bodyHashes := map[string]string{}
	for _, e := range entries {
		bodyHashes[e.key] = HashFuncDecl(e.decl)
	}
	closureHashes := computeClosureHashes(entries, pi)

	// 本轮摘要：先从缓存载入未变函数，脏函数留待迭代填充。
	sums := map[*types.Func]*Summary{}
	for _, e := range entries {
		if dirty != nil && !dirty[e.key] {
			if c, ok := a.cache[e.key]; ok && c != nil {
				sums[e.fn] = c.summary
			}
		}
	}

	prev := map[string]*Summary{}
	diagHold := map[string][]diag.Diagnostic{}
	locHold := map[string][]locDiag{}

	for {
		changed := false
		for _, e := range entries {
			if dirty != nil && !dirty[e.key] {
				continue
			}
			active := map[*types.Func]bool{e.fn: true}
			resolver := func(callee *types.Func) *Summary {
				if active[callee] {
					return nil // 递归活动边：保守未知
				}
				if s, ok := sums[callee]; ok {
					return s
				}
				// 缓存中可能存在（全新指针但同名）。
				if c, ok := a.cache[callee.FullName()]; ok && c != nil {
					return c.summary
				}
				return nil
			}
			env := FuncEnv{
				Func:         e.fn,
				Decl:         e.decl,
				Fset:         pi.Fset,
				Info:         pi.TypeInfo,
				SendStates:   res.SendStates,
				ShowExcluded: a.showExcluded,
				Decls:        declByFn,
			}
			sum, ds, lds := AnalyzeSummary(env, resolver)
			sum.BodyHash = bodyHashes[e.key]
			sums[e.fn] = sum
			diagHold[e.key] = ds
			locHold[e.key] = lds
			if p, ok := prev[e.key]; !ok || !summaryEqual(p, sum) {
				changed = true
			}
			prev[e.key] = sum
		}
		if !changed {
			break
		}
	}

	// 产出：脏函数覆盖缓存；非脏函数原样重放，保证增量结果与冷启动一致。
	var allDiags []diag.Diagnostic
	for _, e := range entries {
		res.Summaries[e.key] = sums[e.fn]
		if dirty == nil || dirty[e.key] {
			a.cache[e.key] = &funcCache{
				hash:    closureHashes[e.key],
				summary: sums[e.fn],
				diags:   locHold[e.key],
				diagFn:  e.key,
			}
			allDiags = append(allDiags, diagHold[e.key]...)
		} else if c, ok := a.cache[e.key]; ok && c != nil {
			allDiags = append(allDiags, locsToDiagnostics(pi.Fset, c.diags, c.diagFn, anchorLines(pi))...)
		}
	}

	// 发送语句通道空值：每轮对全部函数独立重算（仅依赖函数内数据流），
	// 不跨轮缓存 token.Pos，避免新 FileSet 下位置失配。
	res.SendStates = ComputeSendStates(pi)
	for _, e := range entries {
		ast.Inspect(e.decl, func(n ast.Node) bool {
			if sd, ok := n.(*ast.SendStmt); ok {
				res.Sends[sd.Pos()] = sd
			}
			return true
		})
	}

	res.Diagnostics = allDiags
	return res
}

// DirtyFunctions 精确计算需要重算的函数（稳定字符串键）：
// 闭包哈希相对缓存变化的函数（自身或被调链变化）。这些函数本身已经包含了
// “受影响范围”——调用方的闭包哈希会因其被调函数哈希变化而变化。
func (a *Analyzer) DirtyFunctions(pi *loader.PkgInfo) map[string]bool {
	entries := collectFunctions(pi)
	closure := computeClosureHashes(entries, pi)
	dirty := map[string]bool{}
	for _, e := range entries {
		c, ok := a.cache[e.key]
		if !ok || c == nil || c.hash != closure[e.key] {
			dirty[e.key] = true
		}
	}
	return dirty
}
