# nca — nil & concurrency static analyzer (Go)

`nca` is a locally-runnable static analysis service that finds two classes of
defects in Go code **without executing it**:

1. **Null-pointer dereferences** (`nil-deref`) — pointers/interfaces that may
   be nil when dereferenced (`*p`, `p.Field`, `p.Method()`).
2. **Concurrency defects** —
   - `copied-lock`: a value containing `sync.Mutex` / `sync.RWMutex` /
     `sync.WaitGroup` / `sync.Once` / `sync/atomic` types is copied;
   - `unlock-path`: a mutex is not unlocked on every reachable path;
   - `nil-chan-send`: a send to, or closing of, a channel that is nil on the
     reaching path (permanent block / panic).

The analysis is **flow-sensitive**, tracks nilness across functions (return
values, `(T, error)` / `(T, bool)` failure indicators and arguments), supports
**incremental re-analysis** (only affected packages are recomputed) and emits
**deterministic, suppressible** diagnostics.

## Quick start

```bash
go build -o nca ./cmd/nca

# Analyze a module
./nca -root ./path/to/module

# JSON output
./nca -root ./path/to/module -format json

# Turn categories on/off
./nca -root . -only nil-deref
./nca -root . -disable unlock-path,copied-lock

# Audit mode: also show dereferences excluded by guards
./nca -root . -report-guarded

# Cached incremental analysis (identical output to a cold run)
./nca -root . -incremental
```

Diagnostics look like:

```
path/to/file.go:42:9 [nil-deref/certain] dereference of a value that is nil on this path
path/to/file.go:57:2 [unlock-path/suspect] mutex is not unlocked on every reachable path from this Lock (use defer mu.Unlock())
```

Each diagnostic is located by `file:line:column`, carries a category and a
confidence, and (for cross-function findings) a representative call site.

## Confidence levels

| level      | meaning                                                                  |
|------------|--------------------------------------------------------------------------|
| `certain`  | the value is nil on every reaching path (or the lock is copied by value) |
| `possible` | at least one feasible-looking path may see nil                           |
| `suspect`  | a concurrency smell that is usually a bug but can be intentional         |
| `guarded`  | audit-only conclusion: the deref is excluded by an explicit non-nil guard|

## Suppression

Inline comment directives silence specific positions:

```go
p := load()
//nca:ignore nil-deref            <- suppresses nil-deref on the next line
_ = *p

//nca:ignore copied-lock,nil-chan-send   <- multiple categories
mu.Lock()

//nca:ignore                     <- suppress every category on the next line
_ = p.v
```

Rules:

- the directive must sit **on the same line** or **directly above** the
  diagnostic position;
- without a category list it silences **all** categories at that position;
- a category-scoped directive never affects other categories or other
  positions.

## Configuration

| flag                | description                                            |
|---------------------|--------------------------------------------------------|
| `-root DIR`         | module root (default `.`)                              |
| `-format text/json` | output format (default `text`)                         |
| `-only a,b`         | run only the listed categories                         |
| `-disable a,b`      | disable the listed categories globally                 |
| `-enable a,b`       | re-enable categories after earlier flags               |
| `-report-guarded`   | also emit `guarded` audit conclusions                  |
| `-min-confidence`   | `certain` / `possible` / `suspect` floor (default all)   |
| `-serve ADDR`       | run a local HTTP analysis service (`/healthz`, `POST /analyze`) |
| `-incremental`      | use the cached incremental engine                      |
| `-list-categories`  | print supported categories and exit                    |

Programmatic configuration lives in `internal/config` (`config.Config`,
`Enable`/`Disable`/`IsEnabled`, `ReportGuarded`).

## Project layout

```
cmd/nca/              CLI
internal/config/      categories, confidence levels, global switches
internal/loader/      directory scan, AST parsing, go/types type checking,
                      content-hash cache for incremental invalidation
internal/analysis/    flow-sensitive nil lattice, cross-function summaries,
                      nil-deref / copied-lock / unlock-path / nil-chan checks
internal/incremental/ per-package cache with reverse-dependency recomputation
internal/suppress/    //nca:ignore directives
internal/output/      deterministic text/JSON rendering
```

## Tests

```bash
go test ./...
```

Covered areas: direct nil tracking, guards, cross-file/cross-function
propagation, `(T, error)` and `(T, bool)` correlation, parameter sensitivity,
all three concurrency checks, incremental-vs-cold equivalence, suppression
semantics and output ordering stability.

See [DESIGN.md](DESIGN.md) for the precision model, algorithms and known
limitations.
