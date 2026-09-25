// Command nca runs the nil and concurrency static analyzer over a Go module.
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/guoyyit/nil-concurrency-analyzer-go/internal/analysis"
	"github.com/guoyyit/nil-concurrency-analyzer-go/internal/config"
	"github.com/guoyyit/nil-concurrency-analyzer-go/internal/incremental"
	"github.com/guoyyit/nil-concurrency-analyzer-go/internal/loader"
	"github.com/guoyyit/nil-concurrency-analyzer-go/internal/output"
	"github.com/guoyyit/nil-concurrency-analyzer-go/internal/suppress"
)

func main() {
	os.Exit(run())
}

func run() int {
	var (
		root            = flag.String("root", ".", "module root directory to analyze")
		format          = flag.String("format", "text", "output format: text|json")
		disable         = flag.String("disable", "", "comma-separated categories to disable")
		enable          = flag.String("enable", "", "comma-separated categories to enable (with -disable=... use one)")
		only            = flag.String("only", "", "comma-separated categories: run only these")
		incrementalMode = flag.Bool("incremental", false, "use the incremental engine (same results, cached)")
		listCats        = flag.Bool("list-categories", false, "print supported categories and exit")
		guarded         = flag.Bool("report-guarded", false, "also emit guarded (protected) dereference conclusions")
		serveAddr       = flag.String("serve", "", "run as local HTTP service on addr, e.g. :8080")
		minConf         = flag.String("min-confidence", "", "minimum confidence to report: certain|possible|suspect (default all)")
	)
	flag.Parse()

	if *listCats {
		for _, c := range config.AllCategories() {
			fmt.Println(string(c))
		}
		return 0
	}

	cfg := config.Default()
	cfg.ReportGuarded = *guarded
	if *only != "" {
		for _, c := range config.AllCategories() {
			cfg.Disable(c)
		}
		for _, c := range splitList(*only) {
			cfg.Enable(config.Category(c))
		}
	}
	for _, c := range splitList(*disable) {
		cfg.Disable(config.Category(c))
	}
	for _, c := range splitList(*enable) {
		cfg.Enable(config.Category(c))
	}

	var results []*analysis.Result
	var ld *loader.Loader

	if *incrementalMode {
		eng, err := incremental.New(*root, cfg)
		if err != nil {
			fmt.Fprintln(os.Stderr, "nca:", err)
			return 2
		}
		results, err = eng.Analyze()
		if err != nil {
			fmt.Fprintln(os.Stderr, "nca:", err)
			return 2
		}
		ld = engLoader(eng)
	} else {
		var err error
		ld, err = loader.New(*root, cfg)
		if err != nil {
			fmt.Fprintln(os.Stderr, "nca:", err)
			return 2
		}
		if err := ld.LoadAll(); err != nil {
			fmt.Fprintln(os.Stderr, "nca:", err)
			return 2
		}
		var paths []string
		for p := range ld.Packages() {
			paths = append(paths, p)
		}
		results, err = analysis.New(cfg).Analyze(ld, paths)
		if err != nil {
			fmt.Fprintln(os.Stderr, "nca:", err)
			return 2
		}
	}

	if *serveAddr != "" {
		if err := startServer(*serveAddr, cfg); err != nil {
			fmt.Fprintln(os.Stderr, "nca:", err)
			return 1
		}
		return 0
	}

	// Apply confidence floor and comment suppressions.
	sup := suppress.New(ld.Fset(), ld.Packages())
	floor := confidenceRank(config.Confidence(*minConf))
	for _, r := range results {
		r.Diagnostics = filterConfidence(sup.Filter(r.Diagnostics), floor)
	}

	if err := output.Render(os.Stdout, results, output.Format(*format)); err != nil {
		fmt.Fprintln(os.Stderr, "nca:", err)
		return 2
	}
	return 0
}

func splitList(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

// confidenceRank maps a confidence flag to a numeric floor; an empty/unknown
// value means "report everything".
func confidenceRank(c config.Confidence) int {
	switch c {
	case config.Certain:
		return 3
	case config.Possible:
		return 2
	case config.Suspect:
		return 1
	default:
		return 0
	}
}

func diagRank(d *analysis.Diagnostic) int {
	switch d.Confidence {
	case config.Certain:
		return 3
	case config.Possible:
		return 2
	case config.Suspect:
		return 1
	default:
		return 0
	}
}

func filterConfidence(diags []*analysis.Diagnostic, floor int) []*analysis.Diagnostic {
	if floor == 0 {
		return diags
	}
	out := diags[:0]
	for _, d := range diags {
		if diagRank(d) >= floor {
			out = append(out, d)
		}
	}
	return out
}
