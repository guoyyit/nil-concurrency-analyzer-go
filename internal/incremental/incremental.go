// Package incremental caches per-package analysis results and recomputes
// only packages affected by changed inputs. Results are identical to a cold
// full analysis: the same diagnostics, deterministically ordered.
package incremental

import (
	"sort"

	"github.com/guoyyit/nil-concurrency-analyzer-go/internal/analysis"
	"github.com/guoyyit/nil-concurrency-analyzer-go/internal/config"
	"github.com/guoyyit/nil-concurrency-analyzer-go/internal/loader"
)

// Engine is an incremental analysis cache over one module.
type Engine struct {
	dir string
	cfg *config.Config

	ld *loader.Loader
	az *analysis.Analyzer

	// snapshot maps absolute file path -> content hash from the last run.
	snapshot map[string]string
	// cache holds the last diagnostics per package.
	cache map[string][]*analysis.Diagnostic
	// pkgFiles maps import path -> files at last run.
	pkgFiles map[string][]string
}

// New creates an incremental engine rooted at dir.
func New(dir string, cfg *config.Config) (*Engine, error) {
	if cfg == nil {
		cfg = config.Default()
	}
	ld, err := loader.New(dir, cfg)
	if err != nil {
		return nil, err
	}
	return &Engine{
		dir:      dir,
		cfg:      cfg,
		ld:       ld,
		az:       analysis.New(cfg),
		snapshot: map[string]string{},
		cache:    map[string][]*analysis.Diagnostic{},
		pkgFiles: map[string][]string{},
	}, nil
}

// Snapshot describes the current input file set (path -> content hash).
type Snapshot struct {
	Files map[string]string
}

// currentSnapshot hashes every discovered file.
func (e *Engine) currentSnapshot() (map[string]string, error) {
	if err := e.ld.Discover(); err != nil {
		return nil, err
	}
	snap := map[string]string{}
	for _, m := range e.ld.Metas() {
		for _, f := range m.Files {
			snap[f.AbsPath] = f.Hash
		}
	}
	return snap, nil
}

// changedPackages computes packages whose files changed since last run.
func (e *Engine) changedPackages(snap map[string]string) map[string]bool {
	changed := map[string]bool{}
	// Added or modified files.
	for abs, hash := range snap {
		if old, ok := e.snapshot[abs]; !ok || old != hash {
			if ip := e.ld.PackageOfFile(abs); ip != "" {
				changed[ip] = true
			}
		}
	}
	// Removed files.
	for abs := range e.snapshot {
		if _, ok := snap[abs]; !ok {
			if ip := e.pkgFilesOwner(abs); ip != "" {
				changed[ip] = true
			}
		}
	}
	return changed
}

func (e *Engine) pkgFilesOwner(abs string) string {
	for ip, files := range e.pkgFiles {
		for _, f := range files {
			if f == abs {
				return ip
			}
		}
	}
	return ""
}

// reverseDepsClosure expands a changed set to include every local package
// that transitively imports a changed package.
func (e *Engine) reverseDepsClosure(changed map[string]bool) map[string]bool {
	// Build reverse edges.
	rev := map[string][]string{}
	for ip := range e.ld.Metas() {
		for _, dep := range e.ld.Dependencies(ip) {
			rev[dep] = append(rev[dep], ip)
		}
	}
	affected := map[string]bool{}
	queue := make([]string, 0, len(changed))
	for ip := range changed {
		affected[ip] = true
		queue = append(queue, ip)
	}
	for len(queue) > 0 {
		ip := queue[0]
		queue = queue[1:]
		for _, user := range rev[ip] {
			if !affected[user] {
				affected[user] = true
				queue = append(queue, user)
			}
		}
	}
	return affected
}

// Analyze runs cold or incremental analysis and returns diagnostics for the
// whole module, identical to a cold full analysis.
func (e *Engine) Analyze() ([]*analysis.Result, error) {
	snap, err := e.currentSnapshot()
	if err != nil {
		return nil, err
	}
	changed := e.changedPackages(snap)
	affected := e.reverseDepsClosure(changed)

	// Invalidate type-check caches for affected packages and re-check.
	var affectedList []string
	for ip := range affected {
		affectedList = append(affectedList, ip)
	}
	sort.Strings(affectedList)
	if len(affectedList) > 0 {
		var files []string
		for _, ip := range affectedList {
			if m := e.ld.Meta(ip); m != nil {
				for _, f := range m.Files {
					files = append(files, f.AbsPath)
				}
			}
		}
		e.ld.Invalidate(files)
		if err := e.ld.EnsureChecked(affectedList); err != nil {
			return nil, err
		}
	}

	// Analyze affected packages; reuse cache for the rest.
	results, err := e.az.Analyze(e.ld, affectedList)
	if err != nil {
		return nil, err
	}
	for _, r := range results {
		e.cache[r.Package] = r.Diagnostics
	}
	// Drop cache entries for packages that disappeared.
	for ip := range e.cache {
		if e.ld.Meta(ip) == nil {
			delete(e.cache, ip)
		}
	}

	// Update snapshot bookkeeping.
	e.snapshot = snap
	e.pkgFiles = map[string][]string{}
	for _, m := range e.ld.Metas() {
		for _, f := range m.Files {
			e.pkgFiles[m.ImportPath] = append(e.pkgFiles[m.ImportPath], f.AbsPath)
		}
	}

	// Merge results deterministically.
	paths := make([]string, 0, len(e.cache))
	for ip := range e.cache {
		paths = append(paths, ip)
	}
	sort.Strings(paths)
	var out []*analysis.Result
	for _, ip := range paths {
		out = append(out, &analysis.Result{Package: ip, Diagnostics: e.cache[ip]})
	}
	return out, nil
}

// Loader exposes the underlying loader (read-only use).
func (e *Engine) Loader() *loader.Loader { return e.ld }

// AffectedPackages reports which packages would be re-analyzed on the next
// run, for observability and tests.
func (e *Engine) AffectedPackages() ([]string, error) {
	snap, err := e.currentSnapshot()
	if err != nil {
		return nil, err
	}
	changed := e.changedPackages(snap)
	affected := e.reverseDepsClosure(changed)
	out := make([]string, 0, len(affected))
	for ip := range affected {
		out = append(out, ip)
	}
	sort.Strings(out)
	return out, nil
}
