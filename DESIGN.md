# Design: precision model and known limitations

## 1. Pipeline

1. **Load** (`internal/loader`): walk the module root (skipping dot-prefixed,
   `vendor`/`testdata` dirs and `_test.go`), parse with comments, type-check
   each package in dependency order with a custom `go/types` importer that
   serves module packages from cache and falls back to gc export data for the
   standard library / third parties. Parsed files are cached by **content
   hash** (SHA-256) so unchanged files are never re-parsed.
2. **Index** (`internal/analysis/funcs.go`): every named function/method with
   a body is indexed; static callee edges are resolved for plain identifiers
   and concrete method values (dynamic dispatch and function-typed values are
   intentionally not resolved).
3. **Summaries** (`nilanalysis.go`): functions are processed in
   dependency-first order; recursion is handled by a bounded fixpoint
   (6 rounds on the finite nil lattice).
4. **Diagnostic pass**: the body is re-analyzed once per **distinct
   callsite argument-state vector** observed for each function (context
   splitting on the nil lattice), so a nil argument at one callsite cannot
   taint a dereference reached only from a guarded callsite.
5. **Concurrency checks** (`copylock.go`, `unlockpath.go`, `nilchan.go`) run
   over the same indexed program.
6. **Suppression** and **deterministic rendering** are applied last.

## 2. Nil lattice

```
bottom (unreachable)
   |
 Nil ---- NonNil
   \       /
    MayNil        (nil on some path, non-nil on another)
      |
    Unknown       (no information; never reported)
```

- `Unknown` is the top element: joining with `Unknown` yields `Unknown`, so a
  function returning `nil` on one branch and a map lookup (unknown nilness) on
  another is never mislabelled "definitely nil".
- Only `Nil` (→ `certain`) and `MayNil` (→ `possible`) produce a nil-deref
  diagnostic. `Unknown` is silent by design.
- Branch refinement is **authoritative on its path**: the fact taken from
  `p != nil` overrides the join state on the then-path.

Tracked sources of nilness: `nil` literals, `&x`/`&T{}`/composite literals
(non-nil), `new`/`make` (non-nil), multi-result calls, map-index /
type-assertion / channel-receive `,ok` forms, by-value parameters, and a
hand-written stub table for common standard-library constructors.

## 3. Cross-function propagation

- **Return summaries**: per result, merged over exits; a separate
  `successReturns` / `errorReturns` (or bool) split records nilness on the
  success/failure side of an indicator.
- **Failure indicators**: a final `error` result (`nil` = success) or a final
  `bool` result (`true` = success). Correlation is built when the caller binds
  the results (`v, err := f()`) and tests the indicator (`if err != nil`,
  `if !ok`, bare `if err`), including `&&`/`||`/`!` composition and the
  success-branch merge. Ignoring the indicator uses the merged (possibly nil)
  state, so `f, _ := os.Open(...)` followed by `f.Stat()` is reported.
- **Pass-through returns**: `return g()` carries the callee's split into the
  caller's summary (verified for both local functions and stdlib stubs).
- **Parameter identity**: summaries record which results are a direct
  pass-through of a parameter (`func Wrap(p *T) *T { return p }`); at a
  callsite the exact argument state is substituted, instead of an unsound
  "any nil argument widens everything" rule.
- **Method receivers**: pointer/interface receivers are assumed non-nil inside
  the method body; calls on a possibly-nil receiver are reported at the
  callsite instead.
- **Callsite contexts**: for the diagnostic pass each function is analyzed
  with each distinct argument-state vector seen at its callsites. Therefore:
  `use(nil)` against an unguarded deref in `use` is reported, while a caller
  that checks `if t == nil { return }` before calling produces no report.

### Standard library stubs

`internal/analysis/stubs.go` summarizes constructors (`os.Open`, `net.Dial`,
`context.WithCancel`, `regexp.Compile`, …) and the `New*` naming convention.
For `(T, error)` stubs the convention modeled is: `T` is non-nil exactly when
`err == nil`, and nil on the error path. Unknown external functions stay
`Unknown` (no report) to keep the false-positive rate low.

## 4. Concurrency checks

- **copied-lock**: structural scan of types for by-value
  `sync.Mutex/RWMutex/WaitGroup/Cond/Once` and `sync/atomic` value types
  (transitively through value struct fields; pointer fields do not count).
  Sites: by-value parameters/results, assignments, function arguments,
  range values, returns and channel sends. Pointer-to-lock arguments (`&mu`)
  are not flagged; fresh composite literals passed by value are `suspect`.
- **unlock-path**: a light control-flow walker tracks Lock/RLock vs
  Unlock/RUnlock per statically identifiable target (locals, package vars,
  `a.b`-style fields), including branches, loops, switches, selects and
  `defer mu.Unlock()` (covers every path). A lock that remains held on any
  reachable return or fall-off path is reported once per Lock site, at
  `suspect` confidence (locks intentionally held across function boundaries
  are rare but legal).
- **nil-chan-send**: the nil lattice feeds `ch <- v` and `close(ch)` checks;
  `nil` → `certain` (permanent block / panic), `MayNil` → `possible`. A send
  inside a `select` on a nil channel is noted as blocking that case forever.

## 5. Incremental analysis

The engine keeps:

- per-file content hashes;
- the owning package per file;
- diagnostics per analyzed package.

On a run it diffs hashes (added/modified/removed files → owning packages),
computes the **reverse import-dependency closure** (importers transitively
depend on the change), invalidates parse + type-check caches for exactly
those packages, re-checks them, and re-runs analysis only on them. Unaffected
packages' diagnostics are reused from the cache. Cold and incremental outputs
are merged in the same global `(file, line, column, category, message)`
order, so identical inputs produce byte-identical output and incremental
output equals cold output (covered by
`internal/incremental` tests, including a multi-package edit and a
no-change run).

## 6. Known limitations

These are deliberate precision/effort trade-offs:

1. **Aliasing and pointer fields**: the lattice tracks variables, not
   heap objects. Writing through one pointer is not reflected in an alias;
   pointer fields are tracked conservatively as `Unknown` after writes.
2. **Dynamic dispatch**: interface method calls, function-valued variables
   and generic instantiation are not resolved; they contribute `Unknown`.
3. **Context sensitivity**: only argument nilness is split (0-CFA + finite
   lattice contexts); path/heap/return-site sensitivity is not modeled.
   Deeply mixed "nil sentinel return with per-callsite nil check" idioms (a
   function returning `nil` to mean "no fallthrough", whose callers test
   immediately) can produce `possible` noise when the guard is not recognized
   as dominating the deref.
4. **Control flow**: statements are walked with an explicit branch/merge
   walker rather than a full SSA CFG; loops are widened in a single extra
   iteration (the finite lattice reaches its fixed point), and
   `goto`/fall-through `switch` cases are handled conservatively.
5. **Goroutine bodies**: function literals passed to `go` are analyzed only
   for their own intra-closure derefs; captures are not joined with the
   spawning flow (happens-before is not modeled).
6. **Build constraints and cgo** are ignored; one package per directory is
   assumed and `_test.go` files are excluded.
7. **Third-party source** is read through gc export data when available;
   packages that fail to import become empty stubs, which may weaken type
   information locally but never aborts the run.
8. **Copied-lock** follows the standard `noCopy`-style value types; user types
   embedding them are detected, types merely containing a `*sync.Mutex` are
   not (pointer sharing is safe).
9. **unlock-path** does not summarize locking across functions or through
   interfaces; unlocking in a deferred closure that is not the direct
   `defer mu.Unlock()` call may be missed (reported `suspect`).
