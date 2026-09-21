package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

type spotifyConfig struct {
	ClientID   string `yaml:"client_id"`
	PlaylistID string `yaml:"playlist_id"`
}

func loadSpotifyConfig(path string, getenv func(string) string) (spotifyConfig, error) {
	return loadConfig(path, getenv, true)
}

func loadConfig(path string, getenv func(string) string, requirePlaylist bool) (spotifyConfig, error) {
	var config spotifyConfig
	f, err := os.Open(path)
	if err != nil && !os.IsNotExist(err) {
		return config, fmt.Errorf("read Spotify config: %w", err)
	}
	if err == nil {
		defer f.Close()
		decoder := yaml.NewDecoder(f)
		decoder.KnownFields(true)
		if err := decoder.Decode(&config); err != nil && err != io.EOF {
			return config, fmt.Errorf("%s: %w", path, err)
		}
		var extra any
		if err := decoder.Decode(&extra); err != io.EOF {
			return config, fmt.Errorf("%s: expected a single YAML document", path)
		}
	}
	if value := strings.TrimSpace(getenv("SPOTIFY_CLIENT_ID")); value != "" {
		config.ClientID = value
	}
	if value := strings.TrimSpace(getenv("SPOTIFY_PLAYLIST_ID")); value != "" {
		config.PlaylistID = value
	}
	config.ClientID = strings.TrimSpace(config.ClientID)
	config.PlaylistID = strings.TrimSpace(config.PlaylistID)
	if config.ClientID == "" {
		return config, fmt.Errorf("set client_id in %s or SPOTIFY_CLIENT_ID", path)
	}
	if !requirePlaylist {
		return config, nil
	}
	if config.PlaylistID == "" {
		return config, fmt.Errorf("set playlist_id in %s or SPOTIFY_PLAYLIST_ID", path)
	}
	for _, c := range config.PlaylistID {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9') {
			return config, fmt.Errorf("playlist_id / SPOTIFY_PLAYLIST_ID must be a playlist ID, not a URL")
		}
	}
	return config, nil
}
