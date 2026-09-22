package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func planTracks(ids string) []searchTrack {
	tracks := make([]searchTrack, 0, len(ids))
	for _, id := range ids {
		track := candidate(string(id), string(id), "Artist")
		track.Type = "track"
		tracks = append(tracks, track)
	}
	return tracks
}

func applyTestPlan(t *testing.T, current []searchTrack, plan syncPlan) string {
	t.Helper()
	var ids []string
	for _, track := range current {
		ids = append(ids, track.ID)
	}
	for _, op := range plan.Operations {
		switch op.Kind {
		case "remove":
			if op.From < 0 || op.From >= len(ids) || ids[op.From] != op.Entry.Track.ID {
				t.Fatalf("invalid removal: %+v in %v", op, ids)
			}
			ids = append(ids[:op.From], ids[op.From+1:]...)
		case "move":
			if op.From < 0 || op.From >= len(ids) || ids[op.From] != op.Entry.Track.ID {
				t.Fatalf("invalid move: %+v in %v", op, ids)
			}
			id := ids[op.From]
			ids = append(ids[:op.From], ids[op.From+1:]...)
			if op.To < 0 || op.To > len(ids) {
				t.Fatal("invalid target")
			}
			ids = append(ids, "")
			copy(ids[op.To+1:], ids[op.To:])
			ids[op.To] = id
		case "add":
			if op.To < 0 || op.To > len(ids) {
				t.Fatal("invalid insertion")
			}
			ids = append(ids, "")
			copy(ids[op.To+1:], ids[op.To:])
			ids[op.To] = op.Entry.Track.ID
		default:
			t.Fatal(op.Kind)
		}
	}
	return strings.Join(ids, "")
}

func TestBuildPlanCases(t *testing.T) {
	for _, tt := range []struct {
		current, desired        string
		add, remove, move, keep int
	}{
		{"", "", 0, 0, 0, 0}, {"ABA", "ABA", 0, 0, 0, 3}, {"", "AA", 2, 0, 0, 0}, {"AA", "", 0, 2, 0, 0},
		{"AB", "XAB", 1, 0, 0, 2}, {"XAB", "AB", 0, 1, 0, 2}, {"ABC", "CAB", 0, 0, 1, 2},
		{"AA", "A", 0, 1, 0, 1}, {"A", "AA", 1, 0, 0, 1}, {"ABAC", "BADA", 1, 1, 1, 2},
		{"A", "B", 1, 1, 0, 0},
	} {
		t.Run(tt.current+"->"+tt.desired, func(t *testing.T) {
			current, desired := planTracks(tt.current), planTracks(tt.desired)
			plan, err := buildPlan(current, desired)
			if err != nil {
				t.Fatal(err)
			}
			counts := map[string]int{}
			for _, op := range plan.Operations {
				counts[op.Kind]++
			}
			if counts["add"] != tt.add || counts["remove"] != tt.remove || counts["move"] != tt.move || len(plan.Retained) != tt.keep {
				t.Fatalf("%+v", plan)
			}
			if got := applyTestPlan(t, current, plan); got != tt.desired {
				t.Fatalf("got %s", got)
			}
			if !reflect.DeepEqual(current, planTracks(tt.current)) || !reflect.DeepEqual(desired, planTracks(tt.desired)) {
				t.Fatal("mutated input")
			}
			if tt.current == "AA" && tt.desired == "A" && plan.Operations[0].Entry.Original != 1 {
				t.Fatal("did not keep earliest duplicate")
			}
		})
	}
}

func TestPlanExhaustiveSmallSequences(t *testing.T) {
	sequences := []string{""}
	var generate func(string, int)
	generate = func(prefix string, left int) {
		if left == 0 {
			return
		}
		for _, id := range "ABC" {
			next := prefix + string(id)
			sequences = append(sequences, next)
			generate(next, left-1)
		}
	}
	generate("", 4)
	for _, current := range sequences {
		for _, desired := range sequences {
			plan, err := buildPlan(planTracks(current), planTracks(desired))
			if err != nil {
				t.Fatalf("%s -> %s: %v", current, desired, err)
			}
			if got := applyTestPlan(t, planTracks(current), plan); got != desired {
				t.Fatalf("%s -> %s produced %s", current, desired, got)
			}
		}
	}
}

func TestPlanReadOnlyAndSingleFetch(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "GET" || r.URL.Path != "/playlists/list/items" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(500)
			return
		}
		tracks := planTracks("ABAC")
		var items []playlistItem
		for i := range tracks {
			items = append(items, playlistItem{Item: &tracks[i]})
		}
		json.NewEncoder(w).Encode(map[string]any{"items": items, "next": nil})
	}))
	defer server.Close()
	var out bytes.Buffer
	err := planSongs(context.Background(), server.Client(), server.URL, "token", "list", []Song{{"Artist", "B"}, {"Artist", "A"}, {"Artist", "A"}}, strings.NewReader(""), io.Discard, &out)
	if err != nil || calls != 1 {
		t.Fatalf("%v calls=%d", err, calls)
	}
	for _, want := range []string{"DRY RUN", "REMOVE Artist — C", "MOVE Artist — B", "original position", "Summary: 2 retained, 0 added, 1 removed, 1 moved"} {
		if !strings.Contains(out.String(), want) {
			t.Error(out.String(), "missing", want)
		}
	}
}

func TestPlanEmptyAndBlocked(t *testing.T) {
	for _, tt := range []struct {
		name, body    string
		songs         []Song
		wantErr, want string
	}{
		{"empty desired", `{"items":[{"item":{"id":"a","type":"track","name":"A"}}],"next":null}`, nil, "", "1 removed"},
		{"both empty", `{"items":[],"next":null}`, nil, "", "Already correct"},
		{"null", `{"items":[{"item":null}],"next":null}`, nil, "unsupported playlist entry at position 1", ""},
		{"local", `{"items":[{"is_local":true,"item":{"id":"a","type":"track"}}],"next":null}`, nil, "unsupported playlist entry", ""},
		{"episode", `{"items":[{"item":{"id":"a","type":"episode"}}],"next":null}`, nil, "unsupported playlist entry", ""},
		{"missing id", `{"items":[{"item":{"type":"track"}}],"next":null}`, nil, "unsupported playlist entry", ""},
		{"unresolved", `{"items":[],"next":null}`, []Song{{"Artist", "Missing"}}, "no change plan generated", "UNRESOLVED SONGS"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" {
					t.Error("write")
				}
				if r.URL.Path == "/search" {
					io.WriteString(w, `{"tracks":{"items":[]}}`)
					return
				}
				io.WriteString(w, tt.body)
			}))
			defer server.Close()
			var out bytes.Buffer
			err := planSongs(context.Background(), server.Client(), server.URL, "token", "list", tt.songs, strings.NewReader(""), io.Discard, &out)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("%v", err)
				}
				if strings.Contains(out.String(), "DRY RUN") {
					t.Fatal("partial change plan")
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if tt.want != "" && !strings.Contains(out.String(), tt.want) {
				t.Fatal(out.String())
			}
		})
	}
}

func TestPlanSkippedSelectionBlocksChanges(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Error("write request")
		}
		if r.URL.Path == "/playlists/list/items" {
			io.WriteString(w, `{"items":[],"next":null}`)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"tracks": map[string]any{"items": []searchTrack{candidate("a", "Song", "Artist"), candidate("b", "Song", "Artist")}}})
	}))
	defer server.Close()
	var out bytes.Buffer
	err := planSongs(context.Background(), server.Client(), server.URL, "token", "list", []Song{{"Artist", "Song"}}, strings.NewReader("s\n"), io.Discard, &out)
	if err == nil || !strings.Contains(out.String(), "skipped by user") || strings.Contains(out.String(), "DRY RUN") {
		t.Fatalf("%v %s", err, out.String())
	}
}

func TestPlanRejectsMalformedPlaylist(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{}`) }))
	defer server.Close()
	var out bytes.Buffer
	err := planSongs(context.Background(), server.Client(), server.URL, "token", "list", nil, strings.NewReader(""), io.Discard, &out)
	if err == nil || !strings.Contains(err.Error(), "missing items array") || out.Len() != 0 {
		t.Fatalf("%v %s", err, out.String())
	}
}

func TestPlanReadableFormatting(t *testing.T) {
	plan, err := buildPlan(planTracks("ABAC"), planTracks("BADA"))
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := printPlan(&out, plan); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{
		"PROPOSED CHANGES (3)\n--------------------\n",
		"\n  1. REMOVE Artist — C\n     Position: 4 → removed (original position 4)\n     Track ID: C\n",
		"\n  2. MOVE Artist — B\n     Position: 2 → 1 (original position 2)\n     Track ID: B\n",
		"\n  3. ADD Artist — D\n     Position: insert at 3\n     Track ID: D\n",
		"RETAINED (2) — NO EXPLICIT MOVE NEEDED",
		"Position: original 1 → desired 2",
		"Summary: 2 retained, 1 added, 1 removed, 1 moved\n",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
	if strings.Contains(text, "\x1b") {
		t.Fatal("unexpected terminal escape sequences")
	}
}
