package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestPlaylistPagination(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "GET" || r.URL.Path != "/playlists/abc/items" || r.Header.Get("Authorization") != "Bearer token" || r.URL.Query().Get("limit") != "50" {
			t.Errorf("unexpected request: %v", r)
		}
		switch r.URL.Query().Get("offset") {
		case "0":
			io.WriteString(w, `{"items":[{"item":{"type":"track","name":"Dreams","artists":[{"name":"Fleetwood Mac"}]}},{"item":{"type":"track","name":"Dreams","artists":[{"name":"Fleetwood Mac"}]}}],"next":"https://untrusted.invalid/page"}`)
		case "2":
			io.WriteString(w, `{"items":[{"is_local":true,"item":{"type":"track","name":"Demo","artists":[{"name":"A"},{"name":"B"}]}},{"item":null},{"item":{"type":"episode","name":"Podcast"}}],"next":null}`)
		default:
			t.Error("unexpected offset")
			w.WriteHeader(500)
		}
	}))
	defer server.Close()
	got, err := fetchPlaylist(context.Background(), server.Client(), server.URL, "abc", "token")
	want := []string{"Fleetwood Mac — Dreams", "Fleetwood Mac — Dreams", "[local track] A, B — Demo", "[unavailable item]", "[non-song item: episode] Podcast"}
	if err != nil || !reflect.DeepEqual(got, want) || calls != 2 {
		t.Fatalf("%v %v calls=%d", got, err, calls)
	}
}
func TestPlaylistResponses(t *testing.T) {
	for _, tt := range []struct {
		name              string
		status            int
		body, retry, want string
	}{
		{"empty", 200, `{"items":[],"next":null}`, "", ""},
		{"bad JSON", 200, `bad`, "", "invalid Spotify playlist response"},
		{"stalled pagination", 200, `{"items":[],"next":"more"}`, "", "empty page"},
		{"unauthorized", 401, `{}`, "", "authorization expired"},
		{"forbidden", 403, `{}`, "", "access denied"},
		{"not found", 404, `{}`, "", "not found"},
		{"rate limit", 429, `{}`, "42", "Retry-After: 42"},
		{"rate limit no header", 429, `{}`, "", "rate limited"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if tt.retry != "" {
					w.Header().Set("Retry-After", tt.retry)
				}
				w.WriteHeader(tt.status)
				io.WriteString(w, tt.body)
			}))
			defer server.Close()
			got, err := fetchPlaylist(context.Background(), server.Client(), server.URL, "abc", "token")
			if tt.want == "" {
				if err != nil || len(got) != 0 {
					t.Fatalf("%v %v", got, err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("%v; want %s", err, tt.want)
			}
			if calls != 1 {
				t.Fatalf("unexpected retries: %d", calls)
			}
		})
	}
}
func TestSpotifyConfig(t *testing.T) {
	for _, tt := range []struct{ client, playlist, want string }{{"", "", "SPOTIFY_CLIENT_ID"}, {"client", "", "SPOTIFY_PLAYLIST_ID"}, {"client", "https://spotify.com/playlist/abc", "not a URL"}} {
		t.Setenv("SPOTIFY_CLIENT_ID", tt.client)
		t.Setenv("SPOTIFY_PLAYLIST_ID", tt.playlist)
		_, err := loadSpotifyConfig(filepath.Join(t.TempDir(), "spotify.yaml"), os.Getenv)
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Fatal(err)
		}
	}
}

func TestPlaylistFailureDoesNotReturnPartialList(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			io.WriteString(w, `{"items":[{"item":{"type":"track","name":"Song"}}],"next":"more"}`)
			return
		}
		w.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()
	items, err := fetchPlaylist(context.Background(), server.Client(), server.URL, "abc", "token")
	if err == nil || len(items) != 0 || calls != 2 {
		t.Fatalf("items=%v error=%v calls=%d", items, err, calls)
	}
}

func TestPlaylistCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := fetchPlaylist(ctx, http.DefaultClient, "http://127.0.0.1:1", "abc", "token")
	if err == nil || !strings.Contains(err.Error(), "context canceled") {
		t.Fatal(err)
	}
}
