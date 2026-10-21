package main

import (
	"slices"
	"testing"
)

func TestSplitList(t *testing.T) {
	for in, want := range map[string][]string{
		"":        nil,
		" a, ,b ": {"a", "b"},
		"a":       {"a"},
	} {
		if got := splitList(in); !slices.Equal(got, want) {
			t.Errorf("splitList(%q) = %q, want %q", in, got, want)
		}
	}
}
