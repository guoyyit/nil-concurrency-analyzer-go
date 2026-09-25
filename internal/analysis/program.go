// Package analysis runs nil-dereference and concurrency defect checks over a
// set of type-checked Go packages. It performs flow-sensitive
// intraprocedural data flow joined with cross-function summaries computed
// bottom-up over the static call graph.
package analysis

import (
	"go/ast"
	"go/token"
	"go/types"
	"sort"

	"github.com/guoyyit/nil-concurrency-analyzer-go/internal/config"
	"github.com/guoyyit/nil-concurrency-analyzer-go/internal/loader"
)

// Diagnostic is a single reported defect.
type Diagnostic struct {
	Category   config.Category   `json:"category"`
	Confidence config.Confidence `json:"confidence"`
	Pos        loader.FilePos    `json:"pos"`
	Message    string            `json:"message"`
	CallPath   []loader.FilePos  `json:"call_path,omitempty"`
}

// Result holds diagnostics for one analyzed package.
type Result struct {
	Package     string        `json:"package"`
	Diagnostics []*Diagnostic `json:"diagnostics"`
}

// funcDecl is an indexed local function or method.
type funcDecl struct {
	pg      *Program
	obj     *types.Func
	sig     *types.Signature
	decl    *ast.FuncDecl
	pkg     *loader.Package
	pkgPath string
	params  []*types.Var
	results []*types.Var
	edges   []*funcDecl // resolvable local callees
	summary *summary
	diags   []*Diagnostic
}

// Program is the analysis-wide context for one analysis run.
type Program struct {
	cfg  *config.Config
	fset *token.FileSet
	ld   *loader.Loader
	pkgs map[string]*loader.Package

	funcs  map[*types.Func]*funcDecl
	byDecl map[*ast.FuncDecl]*funcDecl
}

// Analyzer runs checks over packages held by a loader.
type Analyzer struct {
	cfg *config.Config
}

// New creates an Analyzer.
func New(cfg *config.Config) *Analyzer {
	if cfg == nil {
		cfg = config.Default()
	}
	return &Analyzer{cfg: cfg}
}

// Analyze runs all enabled checks over targetPkgs (and, when needed, their
// local dependencies). Results are returned only for targetPkgs.
func (a *Analyzer) newProgram(ld *loader.Loader, targetPkgs []string) *Program {
	p := &Program{
		cfg:    a.cfg,
		fset:   ld.Fset(),
		ld:     ld,
		pkgs:   map[string]*loader.Package{},
		funcs:  map[*types.Func]*funcDecl{},
		byDecl: map[*ast.FuncDecl]*funcDecl{},
	}
	want := map[string]bool{}
	var add func(string)
	add = func(path string) {
		if want[path] {
			return
		}
		pkg := ld.Checked(path)
		if pkg == nil {
			return
		}
		want[path] = true
		p.pkgs[path] = pkg
		for _, d := range ld.Dependencies(path) {
			add(d)
		}
	}
	for _, path := range targetPkgs {
		add(path)
	}
	_ = targetPkgs
	return p
}

// Analyze runs all enabled checks over targetPkgs.
func (a *Analyzer) Analyze(ld *loader.Loader, targetPkgs []string) ([]*Result, error) {
	p := a.newProgram(ld, targetPkgs)
	targetSet := map[string]bool{}
	for _, path := range targetPkgs {
		targetSet[path] = true
	}

	p.indexFunctions()
	p.analyzeNil()
	p.analyzeConcurrency()
	return p.collectResults(targetSet), nil
}

// Analysis steps are implemented in funcs.go, nilanalysis.go and
// concurrency.go.

func (p *Program) collectResults(target map[string]bool) []*Result {
	byPkg := map[string][]*Diagnostic{}
	for _, f := range p.funcs {
		if !target[f.pkgPath] {
			continue
		}
		byPkg[f.pkgPath] = append(byPkg[f.pkgPath], dedupDiags(f.diags)...)
	}
	order := make([]string, 0, len(byPkg))
	for path := range byPkg {
		order = append(order, path)
	}
	sort.Strings(order)
	out := make([]*Result, 0, len(order))
	for _, path := range order {
		sortDiagnostics(byPkg[path])
		out = append(out, &Result{Package: path, Diagnostics: byPkg[path]})
	}
	return out
}

// sortDiagnostics orders by file, line, column, then category for stability.
func sortDiagnostics(ds []*Diagnostic) {
	sort.SliceStable(ds, func(i, j int) bool {
		a, b := ds[i].Pos, ds[j].Pos
		if a.File != b.File {
			return a.File < b.File
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		if a.Column != b.Column {
			return a.Column < b.Column
		}
		if ds[i].Category != ds[j].Category {
			return ds[i].Category < ds[j].Category
		}
		return ds[i].Message < ds[j].Message
	})
}

// analyzeProgram builds and runs a Program, exposed for tests.
func (a *Analyzer) analyzeProgram(ld *loader.Loader, targetPkgs []string) *Program {
	p := a.newProgram(ld, targetPkgs)
	p.indexFunctions()
	p.analyzeNil()
	return p
}
