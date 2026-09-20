package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRun(t *testing.T) {
	for _, tt := range []struct{ name, input, output string }{
		{"songs", "- artist: Prince\n  title: Purple Rain\n", "1. Prince — Purple Rain\nTotal: 1 songs\n"},
		{"blank", "", "Total: 0 songs\n"},
		{"empty list", "[]", "Total: 0 songs\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "songs.yaml")
			if err := os.WriteFile(path, []byte(tt.input), 0600); err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			if err := run([]string{path}, &out); err != nil {
				t.Fatal(err)
			}
			if out.String() != tt.output {
				t.Fatalf("got %q; want %q", out.String(), tt.output)
			}
		})
	}
}

func TestRunErrors(t *testing.T) {
	var out bytes.Buffer
	for _, args := range [][]string{nil, {"one", "two"}} {
		if err := run(args, &out); err == nil || !strings.Contains(err.Error(), "usage:") {
			t.Fatalf("got %v; want usage error", err)
		}
	}
	if err := run([]string{filepath.Join(t.TempDir(), "missing.yaml")}, &out); err == nil {
		t.Fatal("expected file error")
	}
	path := filepath.Join(t.TempDir(), "invalid.yaml")
	if err := os.WriteFile(path, []byte("- artist: Prince\n  title: Purple Rain\n- title: Halo"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{path}, &out); err == nil || !strings.Contains(err.Error(), path+": song 2:") {
		t.Fatalf("got %v; want filename and song number", err)
	}
	if out.Len() != 0 {
		t.Fatalf("unexpected output on failure: %q", out.String())
	}
}
