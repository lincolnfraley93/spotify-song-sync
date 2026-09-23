package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func candidate(id, title string, artists ...string) searchTrack {
	track := searchTrack{ID: id, Name: title}
	track.Album.Name = "Album"
	for _, name := range artists {
		track.Artists = append(track.Artists, struct {
			Name string `json:"name"`
		}{name})
	}
	return track
}
func TestStrictMatching(t *testing.T) {
	song := Song{Artist: "Beyoncé", Title: "Halo"}
	for _, tt := range []struct {
		name   string
		tracks []searchTrack
		status string
		count  int
	}{
		{"empty", nil, "unmatched", 0},
		{"exact", []searchTrack{candidate("a", "Halo", "Beyoncé")}, "matched", 1},
		{"case and whitespace", []searchTrack{candidate("a", " HALO \n", " BEYONCÉ ")}, "matched", 1},
		{"credited collaborator", []searchTrack{candidate("a", "Halo", "Other", "Beyoncé")}, "matched", 1},
		{"wrong artist", []searchTrack{candidate("a", "Halo", "Other")}, "unmatched", 0},
		{"accent", []searchTrack{candidate("a", "Halo", "Beyonce")}, "unmatched", 0},
		{"punctuation", []searchTrack{candidate("a", "Halo!", "Beyoncé")}, "unmatched", 0},
		{"live", []searchTrack{candidate("a", "Halo - Live", "Beyoncé")}, "unmatched", 0},
		{"remaster", []searchTrack{candidate("a", "Halo - Remastered", "Beyoncé")}, "unmatched", 0},
		{"multiple releases", []searchTrack{candidate("a", "Halo", "Beyoncé"), candidate("b", "Halo", "Beyoncé")}, "ambiguous", 2},
		{"repeated ID", []searchTrack{candidate("a", "Halo", "Beyoncé"), candidate("a", "Halo", "Beyoncé")}, "matched", 1},
		{"missing ID", []searchTrack{candidate("", "Halo", "Beyoncé")}, "unmatched", 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := matchSong(song, tt.tracks)
			if got.Status != tt.status || len(got.Matches) != tt.count || got.Song != song {
				t.Fatalf("%+v", got)
			}
		})
	}
	if !sameText("Purple   Rain", " purple\tRAIN ") {
		t.Fatal("whitespace normalization")
	}
}

func TestSearchAndResolve(t *testing.T) {
	songs := []Song{{"A & B", "Say \"Hello\""}, {"Other", "Second"}, {"A & B", "Say \"Hello\""}}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls >= len(songs) {
			t.Error("unexpected pagination or retry")
			w.WriteHeader(500)
			return
		}
		song := songs[calls]
		calls++
		if r.Method != "GET" || r.URL.Path != "/search" || r.Header.Get("Authorization") != "Bearer test-token" {
			t.Error("bad request")
		}
		q := r.URL.Query()
		if q.Get("type") != "track" || q.Get("limit") != "10" {
			t.Error(q)
		}
		want := `artist:"A & B" track:"Say \"Hello\""`
		if song.Artist == "Other" {
			want = `artist:"Other" track:"Second"`
		}
		if q.Get("q") != want {
			t.Errorf("query %q; want %q", q.Get("q"), want)
		}
		json.NewEncoder(w).Encode(map[string]any{"tracks": map[string]any{"items": []searchTrack{candidate("id", song.Title, song.Artist)}, "next": "more"}})
	}))
	defer server.Close()
	results, err := resolveSongs(context.Background(), server.Client(), server.URL, "test-token", songs)
	if err != nil || len(results) != 3 || calls != 3 {
		t.Fatalf("%v %v calls=%d", results, err, calls)
	}
	for i, result := range results {
		if result.Song != songs[i] || result.Status != "matched" {
			t.Fatal(result)
		}
	}
	var out bytes.Buffer
	if err := printResolutions(&out, results); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"matched: 3", "album: Album", "ID: id", "https://open.spotify.com/track/id", "first ten"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %s", want)
		}
	}
}

func TestSearchFailures(t *testing.T) {
	for _, tt := range []struct {
		status            int
		body, retry, want string
	}{
		{401, `{}`, "", "HTTP 401"}, {403, `{}`, "", "HTTP 403"}, {429, `{}`, "12", "Retry-After: 12"}, {429, `{}`, "", "rate limited"},
		{200, `bad`, "", "invalid Spotify search response"}, {200, `{}`, "", "missing tracks.items"},
	} {
		calls := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			w.Header().Set("Retry-After", tt.retry)
			w.WriteHeader(tt.status)
			io.WriteString(w, tt.body)
		}))
		results, err := resolveSongs(context.Background(), server.Client(), server.URL, "token", []Song{{"A", "B"}, {"C", "D"}})
		server.Close()
		if err == nil || !strings.Contains(err.Error(), tt.want) || !strings.Contains(err.Error(), "song 1") || len(results) != 0 || calls != 1 {
			t.Fatalf("%v %v calls=%d", results, err, calls)
		}
	}
}

func TestResolveInputBeforeAuthentication(t *testing.T) {
	for _, tt := range []struct {
		body  string
		valid bool
	}{{"", true}, {"[]", true}, {"- artist: Someone", false}} {
		path := filepath.Join(t.TempDir(), "songs.yaml")
		if err := os.WriteFile(path, []byte(tt.body), 0600); err != nil {
			t.Fatal(err)
		}
		var out, prompts bytes.Buffer
		err := runResolve(path, &out, &prompts)
		if (err == nil) != tt.valid || prompts.Len() != 0 {
			t.Fatalf("error=%v prompts=%q", err, prompts.String())
		}
		if tt.valid && !strings.Contains(out.String(), "Total: 0 songs") {
			t.Fatal(out.String())
		}
		if !tt.valid && out.Len() != 0 {
			t.Fatal("partial output")
		}
	}
}
func TestResolveConfigDoesNotRequirePlaylist(t *testing.T) {
	config, err := loadConfig(filepath.Join(t.TempDir(), "missing.yaml"), func(key string) string {
		if key == "SPOTIFY_CLIENT_ID" {
			return "client"
		}
		return ""
	}, false)
	if err != nil || config.ClientID != "client" {
		t.Fatalf("%+v %v", config, err)
	}
}
func TestResolveLaterFailure(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			io.WriteString(w, `{"tracks":{"items":[]}}`)
			return
		}
		w.WriteHeader(500)
	}))
	defer server.Close()
	results, err := resolveSongs(context.Background(), server.Client(), server.URL, "token", []Song{{"A", "B"}, {"C", "D"}})
	if err == nil || len(results) != 0 || !strings.Contains(err.Error(), "song 2") {
		t.Fatalf("%v %v", results, err)
	}
}

func TestISRCGrouping(t *testing.T) {
	const code = "USABC1200001"
	for _, tt := range []struct {
		name   string
		codes  []string
		status string
	}{
		{"same recording", []string{code, code, code}, "matched"},
		{"normalized", []string{code, "  usabc1200001 \n"}, "matched"},
		{"different recordings", []string{code, "USABC1200002"}, "ambiguous"},
		{"mixed missing", []string{code, ""}, "ambiguous"},
		{"first missing", []string{"", code}, "ambiguous"},
		{"all missing", []string{"", ""}, "ambiguous"},
		{"malformed", []string{code, "invalid"}, "ambiguous"},
		{"same malformed", []string{"invalid", "invalid"}, "ambiguous"},
		{"single missing", []string{""}, "matched"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			tracks := make([]searchTrack, len(tt.codes))
			for i, isrc := range tt.codes {
				tracks[i] = candidate(string(rune('a'+i)), "Halo", "Beyoncé")
				tracks[i].ExternalIDs.ISRC = isrc
			}
			result := matchSong(Song{"Beyoncé", "Halo"}, tracks)
			if result.Status != tt.status || len(result.Matches) != len(tracks) {
				t.Fatalf("%+v", result)
			}
			var out bytes.Buffer
			if err := printResolutions(&out, []resolution{result}); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), "ISRC:") {
				t.Fatal(out.String())
			}
			if tt.status == "matched" && len(tracks) > 1 && !strings.Contains(out.String(), "no preferred release selected") {
				t.Fatal(out.String())
			}
		})
	}
}

func TestISRCDoesNotBypassStrictMatching(t *testing.T) {
	for _, track := range []searchTrack{candidate("a", "Halo - Live", "Beyoncé"), candidate("b", "Halo", "Other")} {
		track.ExternalIDs.ISRC = "USABC1200001"
		exact := candidate("c", "Halo", "Beyoncé")
		exact.ExternalIDs.ISRC = track.ExternalIDs.ISRC
		result := matchSong(Song{"Beyoncé", "Halo"}, []searchTrack{track, exact})
		if result.Status != "matched" || len(result.Matches) != 1 || result.Matches[0].ID != "c" {
			t.Fatalf("%+v", result)
		}
	}
}

func TestSearchDecodesISRC(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"tracks":{"items":[{"id":"a","name":"Halo","artists":[{"name":"Beyoncé"}],"external_ids":{"isrc":"USABC1200001"}},{"id":"b","name":"Halo","artists":[{"name":"Beyoncé"}],"external_ids":{"isrc":"USABC1200001"}}]}}`)
	}))
	defer server.Close()
	tracks, err := searchTracks(context.Background(), server.Client(), server.URL, "token", Song{"Beyoncé", "Halo"})
	if err != nil {
		t.Fatal(err)
	}
	result := matchSong(Song{"Beyoncé", "Halo"}, tracks)
	if result.Status != "matched" || len(result.Matches) != 2 || !strings.Contains(result.Reason, "USABC1200001") {
		t.Fatalf("%+v", result)
	}
}

func TestISRCDominance(t *testing.T) {
	for _, tt := range []struct {
		name     string
		sizes    []int
		selected int
	}{
		{"Dying in Your Arms", []int{4, 1}, 4},
		{"Ascendancy", []int{2, 1}, 2},
		{"multiple singletons", []int{3, 1, 1}, 3},
		{"tied repeated groups", []int{2, 2}, 0},
		{"tied singletons", []int{1, 1}, 0},
		{"no majority", []int{2, 1, 1}, 0},
		{"repeated alternative", []int{4, 2}, 0},
		{"winner after singleton", []int{1, 4}, 4},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var tracks []searchTrack
			for group, size := range tt.sizes {
				for n := 0; n < size; n++ {
					track := candidate(string(rune('a'+len(tracks))), tt.name, "Artist")
					track.ExternalIDs.ISRC = "USABC120000" + string(rune('1'+group))
					tracks = append(tracks, track)
				}
			}
			result := matchSong(Song{"Artist", tt.name}, tracks)
			if tt.selected == 0 {
				if result.Status != "ambiguous" || len(result.Matches) != len(tracks) || len(result.Alternatives) != 0 {
					t.Fatalf("%+v", result)
				}
				return
			}
			if result.Status != "matched" || len(result.Matches) != tt.selected || len(result.Alternatives) != len(tracks)-tt.selected {
				t.Fatalf("%+v", result)
			}
			var out bytes.Buffer
			if err := printResolutions(&out, []resolution{result}); err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{"matched by ISRC group dominance", "Selected group:", "Competing candidates (not selected):", "heuristic", "no preferred release selected"} {
				if !strings.Contains(out.String(), want) {
					t.Errorf("missing %q", want)
				}
			}
			for _, track := range tracks {
				if !strings.Contains(out.String(), "ID: "+track.ID+" |") {
					t.Error("lost candidate", track.ID)
				}
			}
		})
	}
}

func TestDominanceConservativeBoundaries(t *testing.T) {
	a := candidate("a", "Song", "Artist")
	a.ExternalIDs.ISRC = "USABC1200001"
	b := candidate("b", "Song", "Artist")
	b.ExternalIDs.ISRC = a.ExternalIDs.ISRC
	c := candidate("c", "Song", "Artist")
	c.ExternalIDs.ISRC = "USABC1200002"
	for _, unknown := range []string{"", "invalid"} {
		d := candidate("d", "Song", "Artist")
		d.ExternalIDs.ISRC = unknown
		got := matchSong(Song{"Artist", "Song"}, []searchTrack{a, b, c, d})
		if got.Status != "ambiguous" {
			t.Fatalf("unknown ISRC allowed dominance: %+v", got)
		}
	}
	if got := matchSong(Song{"Artist", "Song"}, []searchTrack{a, a, c}); got.Status != "ambiguous" {
		t.Fatalf("duplicate ID inflated count: %+v", got)
	}
	b.Name = "Song - Live"
	if got := matchSong(Song{"Artist", "Song"}, []searchTrack{a, b, c}); got.Status != "ambiguous" {
		t.Fatalf("title filter bypassed: %+v", got)
	}
	b.Name = "Song"
	b.Artists[0].Name = "Other"
	if got := matchSong(Song{"Artist", "Song"}, []searchTrack{a, b, c}); got.Status != "ambiguous" {
		t.Fatalf("artist filter bypassed: %+v", got)
	}
}

func TestReportAmbiguitiesLast(t *testing.T) {
	results := []resolution{
		{Song: Song{"A", "First"}, Status: "ambiguous", Matches: []searchTrack{candidate("one", "First", "A")}},
		{Song: Song{"B", "Second"}, Status: "matched"},
		{Song: Song{"C", "Third"}, Status: "ambiguous", Matches: []searchTrack{candidate("three", "Third", "C")}},
		{Song: Song{"D", "Fourth"}, Status: "unmatched"},
	}
	var out bytes.Buffer
	if err := printResolutions(&out, results); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	previous := -1
	for _, part := range []string{"2. B — Second", "4. D — Fourth", "AMBIGUOUS SONGS — REVIEW REQUIRED", "1. A — First", "ID: one", "3. C — Third", "ID: three", "Total: 4 songs; matched: 1; ambiguous: 2; unmatched: 1"} {
		position := strings.Index(text, part)
		if position <= previous {
			t.Fatalf("missing or out-of-order %q in:\n%s", part, text)
		}
		previous = position
	}
	if strings.Count(text, "AMBIGUOUS SONGS — REVIEW REQUIRED") != 1 {
		t.Fatal(text)
	}
	if results[0].Song.Title != "First" || results[1].Song.Title != "Second" {
		t.Fatal("report reordered input")
	}
}

func TestReportAmbiguousHeaderOnlyWhenNeeded(t *testing.T) {
	for _, results := range [][]resolution{nil, {{Song: Song{"A", "B"}, Status: "matched"}}, {{Song: Song{"A", "B"}, Status: "unmatched"}}} {
		var out bytes.Buffer
		if err := printResolutions(&out, results); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(out.String(), "AMBIGUOUS SONGS") {
			t.Fatal(out.String())
		}
	}
}

func TestReportUnmatchedSection(t *testing.T) {
	results := []resolution{
		{Song: Song{"A", "One"}, Status: "unmatched", Reason: "no search results"},
		{Song: Song{"B", "Two"}, Status: "matched"},
		{Song: Song{"C", "Three"}, Status: "unmatched", Reason: "no exact match"},
		{Song: Song{"D", "Four"}, Status: "matched"},
	}
	var out bytes.Buffer
	if err := printResolutions(&out, results); err != nil {
		t.Fatal(err)
	}
	previous := -1
	for _, part := range []string{"2. B — Two", "4. D — Four", "UNMATCHED SONGS — REVIEW REQUIRED", "1. A — One", "no search results", "3. C — Three", "no exact match", "Total: 4 songs; matched: 2; ambiguous: 0; unmatched: 2"} {
		position := strings.Index(out.String(), part)
		if position <= previous {
			t.Fatalf("missing or out-of-order %q in %s", part, out.String())
		}
		previous = position
	}
	if strings.Count(out.String(), "UNMATCHED SONGS — REVIEW REQUIRED") != 1 {
		t.Fatal(out.String())
	}
	for _, results := range [][]resolution{nil, {{Status: "matched"}}, {{Status: "ambiguous"}}} {
		out.Reset()
		if err := printResolutions(&out, results); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(out.String(), "UNMATCHED SONGS") {
			t.Fatal(out.String())
		}
	}
}

func TestSemicolonArtistMatching(t *testing.T) {
	for _, tt := range []struct {
		name, input string
		credits     []string
		want        bool
	}{
		{"multiple required", "Inferi;Trevor Strnad", []string{"Inferi", "Trevor Strnad"}, true},
		{"reversed and normalized", " trevor STRNAD ; INFERI ", []string{"Inferi", "Trevor Strnad"}, true},
		{"extra credits", "Inferi;Trevor Strnad", []string{"Guest", "Inferi", "Trevor Strnad"}, true},
		{"missing credit", "Inferi;Trevor Strnad", []string{"Inferi"}, false},
		{"single credited artist", "Trevor Strnad", []string{"Inferi", "Trevor Strnad"}, true},
		{"literal semicolon name", "Inferi;Trevor Strnad", []string{"Inferi;Trevor Strnad"}, true},
		{"empty component", "Inferi;;Trevor Strnad", []string{"Inferi", "Trevor Strnad"}, false},
		{"comma unchanged", "Inferi, Trevor Strnad", []string{"Inferi", "Trevor Strnad"}, false},
		{"ampersand unchanged", "Inferi & Trevor Strnad", []string{"Inferi", "Trevor Strnad"}, false},
		{"feat unchanged", "Inferi feat. Trevor Strnad", []string{"Inferi", "Trevor Strnad"}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			track := candidate("existing", "Behold the Bearer of Light", tt.credits...)
			track.Type = "track"
			song := Song{tt.input, track.Name}
			got := matchSong(song, []searchTrack{track})
			if (got.Status == "matched") != tt.want {
				t.Fatalf("search result: %+v", got)
			}
			if (playlistChoice(song, []playlistItem{{Item: &track}}) != nil) != tt.want {
				t.Fatal("playlist matching disagrees")
			}
		})
	}
	for _, title := range []string{"Behold the Bearer of Light - Live", "Behold the Bearer of Light!"} {
		track := candidate("other", title, "Inferi", "Trevor Strnad")
		track.Type = "track"
		song := Song{"Inferi;Trevor Strnad", "Behold the Bearer of Light"}
		if matchSong(song, []searchTrack{track}).Status != "unmatched" || playlistChoice(song, []playlistItem{{Item: &track}}) != nil {
			t.Fatal("title matching relaxed")
		}
	}
}
