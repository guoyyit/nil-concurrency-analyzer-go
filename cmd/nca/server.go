package main

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/guoyyit/nil-concurrency-analyzer-go/internal/analysis"
	"github.com/guoyyit/nil-concurrency-analyzer-go/internal/config"
	"github.com/guoyyit/nil-concurrency-analyzer-go/internal/incremental"
	"github.com/guoyyit/nil-concurrency-analyzer-go/internal/suppress"
)

// startServer runs a small local HTTP analysis service:
//
//	GET  /healthz                 -> {"status":"ok"}
//	POST /analyze {"root":"..."}  -> JSON diagnostics (incremental, cached
//	                                per root directory)
//
// The same category switches apply via query parameters
// (disable=a,b / only=a,b).
func startServer(addr string, cfg *config.Config) error {
	servers := map[string]*incremental.Engine{}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok"}`))
	})

	mux.HandleFunc("/analyze", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST required", http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			Root string `json:"root"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request: "+err.Error(), http.StatusBadRequest)
			return
		}
		if req.Root == "" {
			req.Root = "."
		}
		runCfg := cloneConfig(cfg, r.URL.Query())
		eng, err := servers[req.Root]
		if err == false {
		}
		_ = eng
		// One engine per root keeps the incremental cache local to a
		// workspace; recreate when the config changed.
		e, err2 := incremental.New(req.Root, runCfg)
		if err2 != nil {
			http.Error(w, err2.Error(), http.StatusInternalServerError)
			return
		}
		servers[req.Root] = e
		results, err3 := e.Analyze()
		if err3 != nil {
			http.Error(w, err3.Error(), http.StatusInternalServerError)
			return
		}
		sup := suppress.New(e.Loader().Fset(), e.Loader().Packages())
		var flat []*analysis.Diagnostic
		for _, res := range results {
			flat = append(flat, sup.Filter(res.Diagnostics)...)
		}
		if flat == nil {
			flat = []*analysis.Diagnostic{}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(flat)
	})

	return http.ListenAndServe(addr, mux)
}

// cloneConfig applies disable/only query parameters to a copy of cfg.
func cloneConfig(base *config.Config, q interface {
	Get(string) string
}) *config.Config {
	c := &config.Config{Enabled: map[config.Category]bool{}, ReportGuarded: base.ReportGuarded}
	for _, cat := range config.AllCategories() {
		c.Enabled[cat] = base.IsEnabled(cat)
	}
	if only := q.Get("only"); only != "" {
		for _, cat := range config.AllCategories() {
			c.Disable(cat)
		}
		for _, name := range strings.Split(only, ",") {
			c.Enable(config.Category(strings.TrimSpace(name)))
		}
	}
	for _, name := range strings.Split(q.Get("disable"), ",") {
		name = strings.TrimSpace(name)
		if name != "" {
			c.Disable(config.Category(name))
		}
	}
	return c
}
