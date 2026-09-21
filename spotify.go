package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"time"
)

const requestTimeout = 30 * time.Second

func runSpotify(out, prompts io.Writer) error {
	config, err := loadSpotifyConfig("spotify.yaml", os.Getenv)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	authCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	client := &http.Client{Timeout: requestTimeout, CheckRedirect: func(req *http.Request, via []*http.Request) error { return http.ErrUseLastResponse }}
	token, err := authenticate(authCtx, client, config.ClientID, prompts)
	if err != nil {
		return err
	}
	cancel()
	items, err := fetchPlaylist(ctx, client, "https://api.spotify.com/v1", config.PlaylistID, token)
	if err != nil {
		return err
	}
	for i, item := range items {
		if _, err := fmt.Fprintf(out, "%d. %s\n", i+1, item); err != nil {
			return err
		}
	}
	_, err = fmt.Fprintf(out, "Total: %d items\n", len(items))
	return err
}

func spotifyHTTPError(operation string, resp *http.Response) error {
	message := fmt.Sprintf("Spotify %s: HTTP %d", operation, resp.StatusCode)
	switch resp.StatusCode {
	case 401:
		message += " (authorization expired or invalid; run the command again)"
	case 403:
		message += " (access denied; check app allowlist, permissions, and playlist ownership/collaboration)"
	case 404:
		message += " (playlist not found or inaccessible)"
	case 429:
		message += " (rate limited)"
		if retry := resp.Header.Get("Retry-After"); retry != "" {
			message += "; Retry-After: " + retry
		}
	}
	return fmt.Errorf("%s", message)
}

type playlistItem struct {
	IsLocal bool         `json:"is_local"`
	Item    *searchTrack `json:"item"`
}

func (p playlistItem) display() string {
	if p.Item == nil {
		return "[unavailable item]"
	}
	name := p.Item.Name
	if name == "" {
		name = "[unavailable title]"
	}
	if p.Item.Type != "track" {
		return "[non-song item: " + p.Item.Type + "] " + name
	}
	artists := make([]string, 0, len(p.Item.Artists))
	for _, a := range p.Item.Artists {
		artists = append(artists, a.Name)
	}
	artist := strings.Join(artists, ", ")
	if artist == "" {
		artist = "[unknown artist]"
	}
	label := artist + " — " + name
	if p.IsLocal {
		label = "[local track] " + label
	}
	return label
}

func fetchPlaylist(ctx context.Context, client *http.Client, base, id, token string) ([]string, error) {
	entries, err := fetchPlaylistItems(ctx, client, base, id, token)
	if err != nil {
		return nil, err
	}
	var items []string
	for _, entry := range entries {
		items = append(items, entry.display())
	}
	return items, nil
}

func fetchPlaylistItems(ctx context.Context, client *http.Client, base, id, token string) ([]playlistItem, error) {
	var items []playlistItem
	for offset := 0; ; {
		endpoint := fmt.Sprintf("%s/playlists/%s/items?limit=50&offset=%d", base, id, offset)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("read Spotify playlist: %w", err)
		}
		if resp.StatusCode != http.StatusOK {
			err := spotifyHTTPError("playlist read", resp)
			resp.Body.Close()
			return nil, err
		}
		var page struct {
			Items []playlistItem `json:"items"`
			Next  *string        `json:"next"`
		}
		err = json.NewDecoder(resp.Body).Decode(&page)
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("invalid Spotify playlist response: %w", err)
		}
		items = append(items, page.Items...)
		if page.Next == nil || *page.Next == "" {
			return items, nil
		}
		if len(page.Items) == 0 {
			return nil, fmt.Errorf("Spotify pagination returned an empty page with more results")
		}
		// Construct the next request ourselves so tokens never follow a response-supplied URL.
		offset += len(page.Items)
	}
}
