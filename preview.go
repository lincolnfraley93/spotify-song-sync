package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

func playlistChoice(song Song, items []playlistItem) *searchTrack {
	for _, item := range items {
		track := item.Item
		if item.IsLocal || track == nil || track.IsLocal || track.Type != "track" || track.ID == "" || !sameText(song.Title, track.Name) {
			continue
		}
		if artistMatches(song.Artist, *track) {
			return track
		}
	}
	return nil
}

func selectableTracks(tracks []searchTrack) []searchTrack {
	var choices []searchTrack
	seen := make(map[string]bool)
	for _, track := range tracks {
		if track.ID == "" || track.IsLocal || (track.Type != "" && track.Type != "track") || seen[track.ID] {
			continue
		}
		seen[track.ID] = true
		choices = append(choices, track)
	}
	return choices
}

// One reader goroutine allows cancellation while waiting on terminal input.
// The done channel releases it between reads; a blocked read ends with stdin/process.
func promptLines(ctx context.Context, in io.Reader) <-chan string {
	lines := make(chan string)
	go func() {
		defer close(lines)
		scanner := bufio.NewScanner(in)
		for scanner.Scan() {
			select {
			case lines <- scanner.Text():
			case <-ctx.Done():
				return
			}
		}
	}()
	return lines
}

func chooseTrack(ctx context.Context, lines <-chan string, prompts io.Writer, song Song, choices []searchTrack) (*searchTrack, error) {
	var menu strings.Builder
	fmt.Fprintf(&menu, "\nSelect a track for %s — %s:\n", song.Artist, song.Title)
	for i, track := range choices {
		var artists []string
		for _, artist := range track.Artists {
			artists = append(artists, artist.Name)
		}
		isrc := normalizedISRC(track.ExternalIDs.ISRC)
		if isrc == "" {
			isrc = "unknown"
		}
		fmt.Fprintf(&menu, "%d. %s — %s | album: %s | ISRC: %s | https://open.spotify.com/track/%s\n", i+1, strings.Join(artists, ", "), track.Name, track.Album.Name, isrc, track.ID)
	}
	if _, err := io.WriteString(prompts, menu.String()); err != nil {
		return nil, err
	}
	for {
		if _, err := fmt.Fprint(prompts, "Choose a track number, [s]kip, or [q]uit: "); err != nil {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case line, ok := <-lines:
			if !ok {
				return nil, fmt.Errorf("selection cancelled: input ended")
			}
			line = strings.TrimSpace(line)
			switch strings.ToLower(line) {
			case "s":
				return nil, nil
			case "q":
				return nil, fmt.Errorf("selection cancelled by user")
			}
			number, err := strconv.Atoi(line)
			if err == nil && number >= 1 && number <= len(choices) {
				chosen := choices[number-1]
				return &chosen, nil
			}
			if _, err := fmt.Fprintln(prompts, "Invalid selection; enter a listed number, s, or q."); err != nil {
				return nil, err
			}
		}
	}
}

func previewSongs(ctx context.Context, client *http.Client, base, token, playlistID string, songs []Song, input io.Reader, prompts io.Writer) ([]resolution, error) {
	if len(songs) == 0 {
		return nil, nil
	}
	items, err := fetchPlaylistItems(ctx, client, base, playlistID, token)
	if err != nil {
		return nil, err
	}
	return resolveAgainstPlaylist(ctx, client, base, token, songs, items, input, prompts)
}

// promptInput owns the one scanner shared by selection and final confirmation.
// It starts lazily, so noninteractive workflows do not consume stdin.
type promptInput struct {
	ctx    context.Context
	reader io.Reader
	lines  <-chan string
}

func (p *promptInput) channel() <-chan string {
	if p.lines == nil {
		p.lines = promptLines(p.ctx, p.reader)
	}
	return p.lines
}

func resolveAgainstPlaylist(ctx context.Context, client *http.Client, base, token string, songs []Song, items []playlistItem, input io.Reader, prompts io.Writer) ([]resolution, error) {
	promptCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	return resolveWithInput(ctx, client, base, token, songs, items, &promptInput{ctx: promptCtx, reader: input}, prompts)
}

func resolveWithInput(ctx context.Context, client *http.Client, base, token string, songs []Song, items []playlistItem, input *promptInput, prompts io.Writer) ([]resolution, error) {
	var results []resolution
	// Preserve Unicode EqualFold behavior by comparing the small in-memory list.
	for i, song := range songs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var result resolution
		reused := false
		for _, previous := range results {
			if sameText(song.Artist, previous.Song.Artist) && sameText(song.Title, previous.Song.Title) {
				result = previous
				result.Song = song
				reused = true
				break
			}
		}
		if reused {
			results = append(results, result)
			continue
		}
		if track := playlistChoice(song, items); track != nil {
			results = append(results, resolution{Song: song, Status: "matched", Reason: "first exact normalized match in playlist order", Selected: track, Source: "playlist", Matches: []searchTrack{*track}})
			continue
		}
		candidates, err := searchTracks(ctx, client, base, token, song)
		if err != nil {
			return nil, fmt.Errorf("song %d: %w", i+1, err)
		}
		result = matchSong(song, selectableTracks(candidates))
		if result.Status == "matched" {
			selected := result.Matches[0]
			result.Selected = &selected
			result.Source = "search"
			result.Reason = strings.ReplaceAll(result.Reason, "; no preferred release selected", "")
			result.Reason += "; first candidate in accepted group selected"
		} else {
			choices := result.Matches
			if result.Status == "unmatched" {
				if len(candidates) == 0 {
					candidates, err = searchQuery(ctx, client, base, token, normalized(song.Artist)+" "+normalized(song.Title))
					if err != nil {
						return nil, fmt.Errorf("song %d fallback search: %w", i+1, err)
					}
				}
				choices = selectableTracks(candidates)
				result.Candidates = choices
			}
			if len(choices) > 0 {
				selected, err := chooseTrack(ctx, input.channel(), prompts, song, choices)
				if err != nil {
					return nil, fmt.Errorf("song %d: %w", i+1, err)
				}
				if selected != nil {
					result.Selected = selected
					result.Source = "manual"
					result.Status = "matched"
					result.Reason = "explicit user selection"
					result.Matches = []searchTrack{*selected}
					result.Alternatives = nil
				} else {
					result.Reason += "; skipped by user"
				}
			}
		}
		results = append(results, result)
	}
	return results, nil
}
