package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"time"
)

type searchTrack struct {
	Type        string `json:"type"`
	IsLocal     bool   `json:"is_local"`
	ExternalIDs struct {
		ISRC string `json:"isrc"`
	} `json:"external_ids"`
	ID      string `json:"id"`
	Name    string `json:"name"`
	Artists []struct {
		Name string `json:"name"`
	} `json:"artists"`
	Album struct {
		Name string `json:"name"`
	} `json:"album"`
}

type resolution struct {
	Selected     *searchTrack
	Source       string
	Candidates   []searchTrack
	Song         Song
	Status       string
	Reason       string
	Matches      []searchTrack
	Alternatives []searchTrack
}

func normalized(text string) string {
	text = strings.NewReplacer("‘", "'", "’", "'", "…", "...").Replace(text)
	return strings.Join(strings.Fields(text), " ")
}
func sameText(a, b string) bool { return strings.EqualFold(normalized(a), normalized(b)) }

// artistMatches preserves literal artist names, then treats semicolon-separated
// names as multiple required credits. Extra Spotify credits are allowed.
func artistMatches(input string, track searchTrack) bool {
	credited := func(name string) bool {
		for _, artist := range track.Artists {
			if sameText(name, artist.Name) {
				return true
			}
		}
		return false
	}
	if credited(input) {
		return true
	}
	names := strings.Split(input, ";")
	if len(names) < 2 {
		return false
	}
	for _, name := range names {
		if strings.TrimSpace(name) == "" || !credited(name) {
			return false
		}
	}
	return true
}

var isrcPattern = regexp.MustCompile(`^[A-Z]{2}[A-Z0-9]{3}[0-9]{7}$`)

func normalizedISRC(value string) string {
	value = strings.ToUpper(strings.TrimSpace(value))
	if !isrcPattern.MatchString(value) {
		return ""
	}
	return value
}

// A shared ISRC identifies a recording, not a preferred release or mastering.
func sharedISRC(tracks []searchTrack) string {
	if len(tracks) == 0 {
		return ""
	}
	isrc := normalizedISRC(tracks[0].ExternalIDs.ISRC)
	if isrc == "" {
		return ""
	}
	for _, track := range tracks[1:] {
		if normalizedISRC(track.ExternalIDs.ISRC) != isrc {
			return ""
		}
	}
	return isrc
}

// Dominance requires a strict majority with only singleton alternatives.
// Counts are distinct track IDs after the strict matching filter.
func dominantISRC(tracks []searchTrack) string {
	counts := make(map[string]int)
	for _, track := range tracks {
		isrc := normalizedISRC(track.ExternalIDs.ISRC)
		if isrc == "" {
			return ""
		}
		counts[isrc]++
	}
	for isrc, count := range counts {
		if count < 2 || count <= len(tracks)/2 {
			continue
		}
		for other, n := range counts {
			if other != isrc && n != 1 {
				return ""
			}
		}
		return isrc
	}
	return ""
}

func matchSong(song Song, candidates []searchTrack) resolution {
	result := resolution{Candidates: candidates, Song: song, Status: "unmatched", Reason: "no exact artist/title match in first ten candidates"}
	if len(candidates) == 0 {
		result.Reason = "no search results"
	}
	seen := make(map[string]bool)
	for _, track := range candidates {
		if track.ID == "" || seen[track.ID] || !sameText(song.Title, track.Name) {
			continue
		}
		if artistMatches(song.Artist, track) {
			result.Matches = append(result.Matches, track)
			seen[track.ID] = true
		}
	}
	switch len(result.Matches) {
	case 0:
	case 1:
		result.Status = "matched"
		result.Reason = "unique exact artist/title match among inspected candidates"
	default:
		result.Status = "ambiguous"
		result.Reason = "multiple exact track matches with different or unknown ISRCs"
		if isrc := sharedISRC(result.Matches); isrc != "" {
			result.Status = "matched"
			result.Reason = fmt.Sprintf("%d Spotify tracks share recording ISRC %s; no preferred release selected", len(result.Matches), isrc)
		} else if isrc := dominantISRC(result.Matches); isrc != "" {
			candidates := result.Matches
			result.Matches = nil
			for _, track := range candidates {
				if normalizedISRC(track.ExternalIDs.ISRC) == isrc {
					result.Matches = append(result.Matches, track)
				} else {
					result.Alternatives = append(result.Alternatives, track)
				}
			}
			result.Status = "matched"
			result.Reason = fmt.Sprintf("matched by ISRC group dominance: %d of %d candidates share %s; heuristic within first ten results; no preferred release selected", len(result.Matches), len(candidates), isrc)
		}
	}
	return result
}

func searchTracks(ctx context.Context, client *http.Client, base, token string, song Song) ([]searchTrack, error) {
	// Quote field values and escape embedded quotes/backslashes before URL encoding.
	quote := func(s string) string {
		return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(normalized(s)) + `"`
	}
	return searchQuery(ctx, client, base, token, "artist:"+quote(song.Artist)+" track:"+quote(song.Title))
}

func searchQuery(ctx context.Context, client *http.Client, base, token, query string) ([]searchTrack, error) {
	q := url.Values{"q": {query}, "type": {"track"}, "limit": {"10"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/search?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Spotify search: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, spotifyHTTPError("search", resp)
	}
	var page struct {
		Tracks *struct {
			Items []searchTrack `json:"items"`
		} `json:"tracks"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
		return nil, fmt.Errorf("invalid Spotify search response: %w", err)
	}
	if page.Tracks == nil || page.Tracks.Items == nil {
		return nil, fmt.Errorf("invalid Spotify search response: missing tracks.items")
	}
	tracks := page.Tracks.Items
	if len(tracks) > 10 {
		tracks = tracks[:10]
	}
	return tracks, nil
}

func resolveSongs(ctx context.Context, client *http.Client, base, token string, songs []Song) ([]resolution, error) {
	results := make([]resolution, 0, len(songs))
	for i, song := range songs {
		tracks, err := searchTracks(ctx, client, base, token, song)
		if err != nil {
			return nil, fmt.Errorf("song %d (%s — %s): %w", i+1, song.Artist, song.Title, err)
		}
		results = append(results, matchSong(song, tracks))
	}
	return results, nil
}

func printResolutions(out io.Writer, results []resolution) error {
	var report strings.Builder
	fmt.Fprintln(&report, "Playlist choices take precedence. Search: first ten candidates; not a catalog-wide uniqueness check.")
	counts := map[string]int{}
	for _, result := range results {
		counts[result.Status]++
	}
	for _, status := range []string{"matched", "unmatched", "ambiguous"} {
		if counts[status] == 0 {
			continue
		}
		if status != "matched" {
			fmt.Fprintf(&report, "\n%s SONGS — REVIEW REQUIRED\n", strings.ToUpper(status))
			fmt.Fprintln(&report, "================================")
		}
		for i, result := range results {
			if result.Status != status {
				continue
			}
			fmt.Fprintf(&report, "%d. %s — %s: %s (%s)\n", i+1, result.Song.Artist, result.Song.Title, result.Status, result.Reason)
			if result.Selected != nil {
				fmt.Fprintf(&report, "   Using ID: %s | source: %s | https://open.spotify.com/track/%s\n", result.Selected.ID, result.Source, result.Selected.ID)
			}
			groups := []struct {
				label  string
				tracks []searchTrack
			}{{"Selected group", result.Matches}, {"Competing candidates (not selected)", result.Alternatives}}
			for _, group := range groups {
				if len(group.tracks) == 0 {
					continue
				}
				if len(result.Alternatives) > 0 {
					fmt.Fprintf(&report, "   %s:\n", group.label)
				}
				for _, track := range group.tracks {
					artists := make([]string, 0, len(track.Artists))
					for _, artist := range track.Artists {
						artists = append(artists, artist.Name)
					}
					isrc := normalizedISRC(track.ExternalIDs.ISRC)
					if isrc == "" {
						isrc = "unknown"
					}
					fmt.Fprintf(&report, "   %s — %s | album: %s | ID: %s | ISRC: %s | https://open.spotify.com/track/%s\n", strings.Join(artists, ", "), track.Name, track.Album.Name, track.ID, isrc, track.ID)
				}
			}
		}
	}
	fmt.Fprintf(&report, "Total: %d songs; matched: %d; ambiguous: %d; unmatched: %d\n", len(results), counts["matched"], counts["ambiguous"], counts["unmatched"])
	_, err := io.WriteString(out, report.String())
	return err
}

func runResolve(path string, out, prompts io.Writer) error {
	return runSongWorkflow(path, out, prompts, false)
}

func runSongWorkflow(path string, out, prompts io.Writer, planning bool) error {
	mode := "resolve"
	if planning {
		mode = "plan"
	}
	return runWorkflow(path, out, prompts, mode)
}

func runWorkflow(path string, out, prompts io.Writer, mode string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	songs, err := parseSongs(f)
	f.Close()
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if len(songs) == 0 && mode == "resolve" {
		return printResolutions(out, nil)
	}
	config, err := loadSpotifyConfig("spotify.yaml", os.Getenv)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	authCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	client := &http.Client{Timeout: requestTimeout, CheckRedirect: func(req *http.Request, via []*http.Request) error { return http.ErrUseLastResponse }}
	var writeScopes []string
	if mode == "sync" {
		writeScopes = []string{publicWriteScope, privateWriteScope}
	}
	token, err := authenticate(authCtx, client, config.ClientID, prompts, writeScopes...)
	if err != nil {
		return err
	}
	cancel()
	if mode == "sync" {
		return syncSongs(ctx, client, "https://api.spotify.com/v1", token, config.PlaylistID, songs, os.Stdin, prompts, out)
	}
	if mode == "plan" {
		return planSongs(ctx, client, "https://api.spotify.com/v1", token, config.PlaylistID, songs, os.Stdin, prompts, out)
	}
	results, err := previewSongs(ctx, client, "https://api.spotify.com/v1", token, config.PlaylistID, songs, os.Stdin, prompts)
	if err != nil {
		return err
	}
	return printResolutions(out, results)
}
