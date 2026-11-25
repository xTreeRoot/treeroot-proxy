package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestColdSourceResolvesActiveRuntimeAndRefusesCycles(t *testing.T) {
	source, target := t.TempDir(), t.TempDir()
	for _, d := range []string{source, target} {
		if e := os.Mkdir(filepath.Join(d, "private"), 0700); e != nil {
			t.Fatal(e)
		}
	}
	if e := privateJSON(filepath.Join(source, "private/runtime-location.json"), map[string]string{"directory": target, "mode": "isolated"}); e != nil {
		t.Fatal(e)
	}
	if got, e := activeRuntime(source); e != nil || got != target {
		t.Fatalf("active directory=%q error=%v", got, e)
	}
	if e := privateJSON(filepath.Join(target, "private/runtime-location.json"), map[string]string{"directory": source, "mode": "isolated"}); e != nil {
		t.Fatal(e)
	}
	if _, e := activeRuntime(source); e == nil {
		t.Fatal("cyclic relocation was accepted")
	}
}
