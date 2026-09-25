// Package config defines analyzer configuration and defect-category switches.
package config

// Category identifies a class of defects detected by the analyzer.
type Category string

// Supported defect categories.
const (
	// NilDereference covers dereferences of values that may be nil.
	NilDereference Category = "nil-deref"
	// CopiedLock covers copying values containing a sync.Locker field
	// (mutex/RWMutex/WaitGroup/Once and pointer-to such).
	CopiedLock Category = "copied-lock"
	// UnlockPath covers mutex holders that are not unlocked on every path.
	UnlockPath Category = "unlock-path"
	// NilChanSend covers sends (and closing) on channels proven nil.
	NilChanSend Category = "nil-chan-send"
)

// AllCategories lists every supported category in stable order.
func AllCategories() []Category {
	return []Category{NilDereference, CopiedLock, UnlockPath, NilChanSend}
}

// Confidence describes how sure the analyzer is about a diagnostic.
type Confidence string

// Confidence levels.
const (
	// Certain: the defect follows from the analysis on all reaching paths.
	Certain Confidence = "certain"
	// Possible: at least one feasible-looking path may trigger it.
	Possible Confidence = "possible"
	// Suspect: a concurrency smell that usually is a bug but may be intended.
	Suspect Confidence = "suspect"
	// Guarded: a dereference site that would otherwise be suspect but is
	// excluded by an explicit non-nil guard dominating it. Only emitted when
	// Config.ReportGuarded is enabled, as an audit/explanation trail.
	Guarded Confidence = "guarded"
)

// Config controls which checks run. The zero value is NOT usable; use Default.
type Config struct {
	// Enabled holds the global on/off switch per category. A category set to
	// false is never reported; an absent key defaults to enabled.
	Enabled map[Category]bool
	// ReportGuarded emits dereference sites proven safe by explicit guards,
	// labeled "guarded". It is off by default.
	ReportGuarded bool
}

// Default returns a configuration with every category enabled.
func Default() *Config {
	c := &Config{Enabled: map[Category]bool{}}
	for _, cat := range AllCategories() {
		c.Enabled[cat] = true
	}
	return c
}

// IsEnabled reports whether a category is switched on. Unknown categories and
// absent keys are treated as enabled.
func (c *Config) IsEnabled(cat Category) bool {
	if c == nil {
		return true
	}
	v, ok := c.Enabled[cat]
	if !ok {
		return true
	}
	return v
}

// Enable turns a category on.
func (c *Config) Enable(cat Category) { c.Enabled[cat] = true }

// Disable turns a category off globally.
func (c *Config) Disable(cat Category) { c.Enabled[cat] = false }

// Description explains a category for help text and documentation.
func (c *Config) Description(cat Category) string {
	switch cat {
	case NilDereference:
		return "pointer/interface dereference that may panic with nil pointer"
	case CopiedLock:
		return "value containing a sync mutex/RWMutex/WaitGroup/Once is copied"
	case UnlockPath:
		return "mutex is locked but not unlocked on every reachable path"
	case NilChanSend:
		return "send to or close of a channel that is nil on the reaching path"
	default:
		return "unknown category"
	}
}
