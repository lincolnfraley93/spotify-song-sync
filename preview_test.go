package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPlaylistChoiceNormalization(t *testing.T) {
	first := candidate("first", "Don’t Stop…", "Guest", "ARTIST")
	first.Type = "track"
	second := first
	second.ID = "second"
	local := first
	local.ID = "local"
	song := Song{" artist ", " don't   stop... "}
	items := []playlistItem{{Item: nil}, {IsLocal: true, Item: &local}, {Item: &first}, {Item: &second}}
	if got := playlistChoice(song, items); got == nil || got.ID != "first" {
		t.Fatalf("%+v", got)
	}
	if playlistChoice(Song{"Artist", "Don't Stop... - Live"}, items) != nil {
		t.Fatal("version ignored")
	}
	if sameText("Beyoncé", "Beyonce") || sameText("Song!", "Song") {
		t.Fatal("over-normalization")
	}
}

func TestPreviewPlaylistBeforeSearch(t *testing.T) {
	calls := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.URL.Path+"?"+r.URL.RawQuery)
		if r.Method != "GET" {
			t.Error("write request")
		}
		if r.URL.Path == "/playlists/list/items" {
			if r.URL.Query().Get("offset") == "0" {
				io.WriteString(w, `{"items":[{"item":null}],"next":"more"}`)
				return
			}
			io.WriteString(w, `{"items":[{"item":{"id":"existing","type":"track","name":"Existing","artists":[{"name":"Artist"}]}}],"next":null}`)
			return
		}
		if len(calls) != 3 {
			t.Error("search before all playlist pages")
		}
		json.NewEncoder(w).Encode(map[string]any{"tracks": map[string]any{"items": []searchTrack{candidate("searched", "New", "Artist")}}})
	}))
	defer server.Close()
	songs := []Song{{"Artist", "Existing"}, {"Artist", "New"}, {"ARTIST", "existing"}}
	var prompts bytes.Buffer
	results, err := previewSongs(context.Background(), server.Client(), server.URL, "token", "list", songs, strings.NewReader(""), &prompts)
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 3 || len(results) != 3 || prompts.Len() != 0 {
		t.Fatalf("calls=%v results=%v prompts=%s", calls, results, prompts.String())
	}
	for i, want := range []string{"existing", "searched", "existing"} {
		if results[i].Selected == nil || results[i].Selected.ID != want || results[i].Song != songs[i] {
			t.Fatal(results[i])
		}
	}
	if results[0].Source != "playlist" || results[1].Source != "search" {
		t.Fatal(results)
	}
}

func TestPreviewInteractive(t *testing.T) {
	for _, tt := range []struct {
		name, input        string
		exact, empty       bool
		wantStatus, wantID string
		wantErr            bool
	}{
		{"ambiguous selection", "9\n2\n", true, false, "matched", "b", false},
		{"nonexact selection", "1\n", false, false, "matched", "a", false},
		{"fallback selection", "2\n", false, true, "matched", "b", false},
		{"skip ambiguous", "s\n", true, false, "ambiguous", "", false},
		{"skip unmatched", "s\n", false, false, "unmatched", "", false},
		{"quit", "q\n", true, false, "", "", true},
		{"EOF", "", true, false, "", "", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			searches := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" {
					t.Error("unexpected method")
				}
				if r.URL.Path == "/playlists/list/items" {
					io.WriteString(w, `{"items":[],"next":null}`)
					return
				}
				searches++
				if tt.empty && searches == 1 {
					io.WriteString(w, `{"tracks":{"items":[]}}`)
					return
				}
				title := "Different"
				if tt.exact {
					title = "Song"
				}
				if searches == 2 && r.URL.Query().Get("q") != "Artist Song" {
					t.Error("unexpected fallback query", r.URL.Query())
				}
				json.NewEncoder(w).Encode(map[string]any{"tracks": map[string]any{"items": []searchTrack{candidate("a", title, "Artist"), candidate("b", title, "Artist")}}})
			}))
			defer server.Close()
			var prompts bytes.Buffer
			results, err := previewSongs(context.Background(), server.Client(), server.URL, "token", "list", []Song{{"Artist", "Song"}, {"Artist", "Song"}}, strings.NewReader(tt.input), &prompts)
			if tt.wantErr {
				if err == nil || results != nil {
					t.Fatalf("%v %v", results, err)
				}
				return
			}
			if err != nil || len(results) != 2 {
				t.Fatalf("%v %v", results, err)
			}
			wantSearches := 1
			if tt.empty {
				wantSearches = 2
			}
			if searches != wantSearches {
				t.Fatalf("searches %d", searches)
			}
			if strings.Count(prompts.String(), "Select a track for") != 1 {
				t.Fatal("duplicate prompt", prompts.String())
			}
			for _, result := range results {
				if result.Status != tt.wantStatus {
					t.Fatal(result)
				}
				if tt.wantID != "" && (result.Selected == nil || result.Selected.ID != tt.wantID || result.Source != "manual") {
					t.Fatal(result)
				}
			}
		})
	}
}

func TestPreviewNoResultsAndFailure(t *testing.T) {
	for _, status := range []int{200, 429} {
		searches := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/playlists/list/items" {
				io.WriteString(w, `{"items":[],"next":null}`)
				return
			}
			searches++
			w.Header().Set("Retry-After", "20")
			w.WriteHeader(status)
			io.WriteString(w, `{"tracks":{"items":[]}}`)
		}))
		var prompts bytes.Buffer
		results, err := previewSongs(context.Background(), server.Client(), server.URL, "token", "list", []Song{{"A", "B"}}, strings.NewReader(""), &prompts)
		server.Close()
		if status == 429 {
			if err == nil || !strings.Contains(err.Error(), "Retry-After: 20") || searches != 1 {
				t.Fatalf("%v calls=%d", err, searches)
			}
			continue
		}
		if err != nil || len(results) != 1 || results[0].Status != "unmatched" || searches != 2 || prompts.Len() != 0 {
			t.Fatalf("%v %v", results, err)
		}
	}
}

func TestChooseTrackCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := chooseTrack(ctx, make(chan string), io.Discard, Song{}, []searchTrack{candidate("a", "Song", "Artist")})
	if err != context.Canceled {
		t.Fatal(err)
	}
}

func TestPreviewRepresentativeAndPlaylistFailure(t *testing.T) {
	for _, fail := range []bool{false, true} {
		searches := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/playlists/list/items" {
				if fail {
					w.WriteHeader(403)
					return
				}
				io.WriteString(w, `{"items":[],"next":null}`)
				return
			}
			searches++
			a := candidate("first", "Song", "Artist")
			a.ExternalIDs.ISRC = "USABC1200001"
			b := a
			b.ID = "second"
			json.NewEncoder(w).Encode(map[string]any{"tracks": map[string]any{"items": []searchTrack{a, b}}})
		}))
		results, err := previewSongs(context.Background(), server.Client(), server.URL, "token", "list", []Song{{"Artist", "Song"}}, strings.NewReader(""), io.Discard)
		server.Close()
		if fail {
			if err == nil || searches != 0 {
				t.Fatal("playlist failure ignored")
			}
			continue
		}
		if err != nil || results[0].Selected.ID != "first" || results[0].Source != "search" {
			t.Fatal(fmt.Sprint(results, err))
		}
	}
}

func TestPreviewEmptyDoesNotFetch(t *testing.T) {
	results, err := previewSongs(context.Background(), nil, "", "", "", nil, strings.NewReader(""), io.Discard)
	if err != nil || len(results) != 0 {
		t.Fatalf("%v %v", results, err)
	}
}

func TestPlaylistUnusableEntries(t *testing.T) {
	for _, kind := range []string{"local", "episode", "missing ID", "missing title", "missing artist"} {
		track := candidate("id", "Song", "Artist")
		track.Type = "track"
		switch kind {
		case "local":
			track.IsLocal = true
		case "episode":
			track.Type = "episode"
		case "missing ID":
			track.ID = ""
		case "missing title":
			track.Name = ""
		case "missing artist":
			track.Artists = nil
		}
		if playlistChoice(Song{"Artist", "Song"}, []playlistItem{{Item: &track}}) != nil {
			t.Fatal("accepted", kind)
		}
	}
}
