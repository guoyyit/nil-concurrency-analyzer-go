package config

import "testing"

func TestSwitches(t *testing.T) {
	c := Default()
	for _, cat := range AllCategories() {
		if !c.IsEnabled(cat) {
			t.Fatalf("%s enabled by default", cat)
		}
	}
	c.Disable(NilDereference)
	if c.IsEnabled(NilDereference) {
		t.Fatal("disable failed")
	}
	c.Enable(NilDereference)
	if !c.IsEnabled(NilDereference) {
		t.Fatal("enable failed")
	}
	// Unknown category defaults to enabled.
	if !c.IsEnabled(Category("nope")) {
		t.Fatal("unknown category must default to enabled")
	}
	// Only list.
	c2 := Default()
	for _, cat := range AllCategories() {
		c2.Disable(cat)
	}
	c2.Enable(UnlockPath)
	if c2.IsEnabled(UnlockPath) != true || c2.IsEnabled(CopiedLock) != false {
		t.Fatal("only semantics broken")
	}
}
