package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadSpotifyConfig(t *testing.T) {
	for _, tt := range []struct{ name, body, clientEnv, playlistEnv, wantClient, wantPlaylist, wantErr string }{
		{"file", "client_id: client\nplaylist_id: abc\n", "", "", "client", "abc", ""},
		{"override", "client_id: client\nplaylist_id: abc\n", "other", "xyz", "other", "xyz", ""},
		{"partial override", "client_id: client\nplaylist_id: abc\n", "other", "", "other", "abc", ""},
		{"missing client", "", "", "", "", "", "SPOTIFY_CLIENT_ID"},
		{"missing playlist", "client_id: client", "", "", "", "", "SPOTIFY_PLAYLIST_ID"},
		{"typo", "client: wrong", "", "", "", "", "field client not found"},
		{"malformed", "[", "", "", "", "", "spotify.yaml"},
		{"multiple documents", "{}\n---\n{}", "", "", "", "", "single YAML document"},
		{"URL", "client_id: client\nplaylist_id: https://example.com", "", "", "", "", "not a URL"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "spotify.yaml")
			if err := os.WriteFile(path, []byte(tt.body), 0600); err != nil {
				t.Fatal(err)
			}
			config, err := loadSpotifyConfig(path, func(key string) string {
				if key == "SPOTIFY_CLIENT_ID" {
					return tt.clientEnv
				}
				return tt.playlistEnv
			})
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("got %v, want %s", err, tt.wantErr)
				}
				return
			}
			if err != nil || config.ClientID != tt.wantClient || config.PlaylistID != tt.wantPlaylist {
				t.Fatalf("%+v %v", config, err)
			}
		})
	}
}
func TestConfigWithoutFile(t *testing.T) {
	config, err := loadSpotifyConfig(filepath.Join(t.TempDir(), "missing.yaml"), func(key string) string { return "abc" })
	if err != nil || config.ClientID != "abc" || config.PlaylistID != "abc" {
		t.Fatalf("%+v %v", config, err)
	}
}
