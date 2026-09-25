package main

import (
	"github.com/guoyyit/nil-concurrency-analyzer-go/internal/incremental"
	"github.com/guoyyit/nil-concurrency-analyzer-go/internal/loader"
)

// engLoader exposes the engine's loader for suppression parsing.
func engLoader(e *incremental.Engine) *loader.Loader { return e.Loader() }
