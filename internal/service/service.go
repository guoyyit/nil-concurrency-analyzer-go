// Package service 把解析、空值分析、并发检查与压制组装为可本地运行的分析服务。
package service

import (
	"go/ast"
	"os"
	"sort"

	"github.com/guoyyit/nil-concurrency-analyzer-go/internal/concurrency"
	"github.com/guoyyit/nil-concurrency-analyzer-go/internal/diag"
	"github.com/guoyyit/nil-concurrency-analyzer-go/internal/loader"
	"github.com/guoyyit/nil-concurrency-analyzer-go/internal/nilflow"
	"github.com/guoyyit/nil-concurrency-analyzer-go/internal/suppress"
)

// Options 控制一次分析的行为。
type Options struct {
	Suppress       suppress.Config
	ShowExcluded   bool // 是否输出被保护分支排除的证据
	ShowSuppressed bool // 是否保留被压制的诊断（置 Suppressed=true）
}

func DefaultOptions() Options {
	return Options{Suppress: suppress.DefaultConfig()}
}

// Service 是有状态的分析服务：同一 Service 上的重复分析只重算受影响范围。
type Service struct {
	loader *loader.Loader
	nilA   *nilflow.Analyzer
	conA   *concurrency.Analyzer
	eng    *suppress.Engine

	// 增量统计（最近一次 Analyze）。
	lastRecomputed int
	lastHitCache   bool
}

// Stats 描述最近一次分析的增量情况。
type Stats struct {
	Recomputed int  // 本次实际重算的函数数
	HitCache   bool // 本次是否完全命中缓存（无文件变化）
}

// Stats 返回最近一次分析的增量统计。
func (s *Service) Stats() Stats {
	return Stats{Recomputed: s.lastRecomputed, HitCache: s.lastHitCache}
}

func New() *Service {
	cfg := suppress.DefaultConfig()
	return &Service{
		loader: loader.New(),
		nilA:   nilflow.NewAnalyzer(),
		conA:   concurrency.NewAnalyzer(),
		eng:    suppress.NewEngine(cfg),
	}
}

// SetConfig 更新全局压制配置。
func (s *Service) SetConfig(cfg suppress.Config) {
	s.eng = suppress.NewEngine(cfg)
}

// Analyze 接收当前完整的文件集合，仅重算受变化影响的范围。
// 同一输入在冷启动与增量重分析下产生完全一致的诊断（排序去重后）。
func (s *Service) Analyze(files []loader.InputFile, opts Options) ([]diag.Diagnostic, error) {
	res, err := s.loader.Load(files)
	if err != nil {
		return nil, err
	}
	s.nilA.SetShowExcluded(opts.ShowExcluded)
	// 每次分析按选项构建压制引擎，确保全局开关即时生效。
	s.eng = suppress.NewEngine(opts.Suppress)

	noChange := len(res.Changed.Changed) == 0 && len(res.Changed.Deleted) == 0
	var allDiags []diag.Diagnostic
	recomputed := 0
	hitCache := noChange

	pkgPaths := make([]string, 0, len(res.Pkgs))
	for p := range res.Pkgs {
		pkgPaths = append(pkgPaths, p)
	}
	sort.Strings(pkgPaths)

	for _, pp := range pkgPaths {
		pi := res.Pkgs[pp]
		// 函数级增量：闭包哈希变化的函数 + 反向调用图上的所有调用方。
		var dirty map[string]bool
		if noChange {
			dirty = map[string]bool{}
		} else {
			dirty = s.nilA.DirtyFunctions(pi)
		}

		nr := s.nilA.Analyze(pi, dirty)
		recomputed += len(dirty)
		cds := s.conA.Analyze(pi, nr)
		allDiags = append(allDiags, nr.Diagnostics...)
		allDiags = append(allDiags, cds...)
	}

	// 行内压制注释。
	var astFiles []*ast.File
	for _, pi := range res.Pkgs {
		for _, fi := range pi.Ordered {
			astFiles = append(astFiles, fi.File)
		}
	}
	s.eng.ParseComments(res.Fset, astFiles)
	allDiags = s.eng.Apply(allDiags, opts.ShowSuppressed)

	if !opts.ShowExcluded {
		filtered := allDiags[:0]
		for _, d := range allDiags {
			if d.Certainty != diag.CertaintyExcluded {
				filtered = append(filtered, d)
			}
		}
		allDiags = filtered
	}

	allDiags = diag.Sort(allDiags)
	s.lastRecomputed = recomputed
	s.lastHitCache = hitCache
	return allDiags, nil
}

// AnalyzePaths 从磁盘读取指定 go 文件并分析。
func (s *Service) AnalyzePaths(paths []string, opts Options) ([]diag.Diagnostic, error) {
	var inputs []loader.InputFile
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		inputs = append(inputs, loader.InputFile{Path: p, Content: string(b)})
	}
	return s.Analyze(inputs, opts)
}
