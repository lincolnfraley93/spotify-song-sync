package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
)

// A stateful server models only documented /items bodies. Any other mutation
// endpoint, extra DELETE property, replacement PUT, or extra request fails tests.
type fakePlaylist struct {
	t              *testing.T
	tracks         []searchTrack
	version        int
	writes         []string
	requests       int
	metadataReads  int
	metadataHook   func(*fakePlaylist)
	failWrite      int
	failureStatus  int
	malformedWrite int
	wrongResult    int
	loseResponse   int
	failVerify     bool
	writeSnapshot  string
	search         []searchTrack
}

func (f *fakePlaylist) snapshot() string { return fmt.Sprintf("snapshot-%d", f.version) }
func (f *fakePlaylist) serve(w http.ResponseWriter, r *http.Request) {
	f.requests++
	if r.Header.Get("Authorization") != "Bearer token" {
		f.t.Error("missing bearer token")
	}
	switch {
	case r.Method == "GET" && r.URL.Path == "/playlists/list":
		f.metadataReads++
		if f.metadataHook != nil {
			f.metadataHook(f)
		}
		if f.failVerify && len(f.writes) > 0 {
			w.WriteHeader(503)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"name": "Test Playlist", "snapshot_id": f.snapshot()})
		return
	case r.Method == "GET" && r.URL.Path == "/playlists/list/items":
		items := make([]playlistItem, 0, len(f.tracks))
		for i := range f.tracks {
			items = append(items, playlistItem{Item: &f.tracks[i]})
		}
		json.NewEncoder(w).Encode(map[string]any{"items": items, "next": nil})
		return
	case r.Method == "GET" && r.URL.Path == "/search":
		tracks := f.search
		if tracks == nil {
			tracks = []searchTrack{}
		}
		json.NewEncoder(w).Encode(map[string]any{"tracks": map[string]any{"items": tracks}})
		return
	}
	if r.URL.Path != "/playlists/list/items" {
		f.t.Errorf("unexpected path %s", r.URL.Path)
		w.WriteHeader(400)
		return
	}
	f.writes = append(f.writes, r.Method)
	if f.failWrite == len(f.writes) {
		status := f.failureStatus
		if status == 0 {
			status = 429
		}
		w.Header().Set("Retry-After", "17")
		w.WriteHeader(status)
		return
	}
	if r.Header.Get("Content-Type") != "application/json" {
		f.t.Error("missing JSON content type")
	}
	var body map[string]json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		f.t.Error(err)
		w.WriteHeader(400)
		return
	}
	switch r.Method {
	case "DELETE":
		if len(body) != 2 {
			f.t.Error("unexpected DELETE properties", body)
		}
		var items []map[string]string
		json.Unmarshal(body["items"], &items)
		var snapshot string
		json.Unmarshal(body["snapshot_id"], &snapshot)
		if snapshot != f.snapshot() {
			f.t.Error("wrong DELETE snapshot")
		}
		if len(items) != 1 || len(items[0]) != 1 {
			f.t.Error("DELETE must only specify uri", items)
			w.WriteHeader(400)
			return
		}
		id := strings.TrimPrefix(items[0]["uri"], "spotify:track:")
		next := []searchTrack{}
		for _, track := range f.tracks {
			if track.ID != id {
				next = append(next, track)
			}
		}
		f.tracks = next
	case "PUT":
		if len(body) != 4 || body["uris"] != nil {
			f.t.Error("replacement or unexpected PUT", body)
		}
		var start, before, length int
		json.Unmarshal(body["range_start"], &start)
		json.Unmarshal(body["insert_before"], &before)
		json.Unmarshal(body["range_length"], &length)
		var snapshot string
		json.Unmarshal(body["snapshot_id"], &snapshot)
		if snapshot != f.snapshot() || length != 1 || before >= start {
			f.t.Error("incorrect reorder body", body)
		}
		track := f.tracks[start]
		copy(f.tracks[before+1:start+1], f.tracks[before:start])
		f.tracks[before] = track
	case "POST":
		if len(body) != 2 || body["snapshot_id"] != nil {
			f.t.Error("unexpected ADD properties", body)
		}
		var uris []string
		var position int
		json.Unmarshal(body["uris"], &uris)
		json.Unmarshal(body["position"], &position)
		if len(uris) != 1 || !strings.HasPrefix(uris[0], "spotify:track:") {
			f.t.Error("invalid URI", uris)
			w.WriteHeader(400)
			return
		}
		track := planTracks(strings.TrimPrefix(uris[0], "spotify:track:"))[0]
		f.tracks = append(f.tracks, searchTrack{})
		copy(f.tracks[position+1:], f.tracks[position:len(f.tracks)-1])
		f.tracks[position] = track
	default:
		f.t.Error("unexpected method", r.Method)
		w.WriteHeader(400)
		return
	}
	f.version++
	if f.wrongResult == len(f.writes) {
		f.tracks = append(f.tracks, planTracks("Z")...)
	}
	if f.loseResponse == len(f.writes) {
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			f.t.Error(err)
		} else {
			conn.Close()
		}
		return
	}
	if r.Method == "POST" {
		w.WriteHeader(201)
	}
	if f.malformedWrite == len(f.writes) {
		io.WriteString(w, `{}`)
		return
	}
	snapshot := f.snapshot()
	if f.writeSnapshot != "" {
		snapshot = f.writeSnapshot
	}
	json.NewEncoder(w).Encode(map[string]string{"snapshot_id": snapshot})
}
func startFake(t *testing.T, current string) (*fakePlaylist, *httptest.Server) {
	t.Helper()
	f := &fakePlaylist{t: t, tracks: planTracks(current)}
	s := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(s.Close)
	return f, s
}
func preparedFor(t *testing.T, current, desired string) preparedPlan {
	t.Helper()
	c, d := planTracks(current), planTracks(desired)
	p, err := buildPlan(c, d)
	if err != nil {
		t.Fatal(err)
	}
	return preparedPlan{Current: c, Desired: d, Plan: p}
}
func idsOf(tracks []searchTrack) string {
	var ids []string
	for _, track := range tracks {
		ids = append(ids, track.ID)
	}
	return strings.Join(ids, "")
}

func TestSyncApplyOperations(t *testing.T) {
	for _, tt := range []struct {
		current, desired string
		methods          []string
	}{
		{"ABAC", "BADA", []string{"DELETE", "PUT", "POST"}},
		{"A", "AAA", []string{"POST", "POST"}},
		{"ABC", "", []string{"DELETE", "DELETE", "DELETE"}},
		{"ABAC", "BAAC", []string{"PUT"}},
	} {
		t.Run(tt.current+"->"+tt.desired, func(t *testing.T) {
			f, s := startFake(t, tt.current)
			var out bytes.Buffer
			err := applySyncPlan(context.Background(), s.Client(), s.URL, "token", "list", f.snapshot(), preparedFor(t, tt.current, tt.desired), &out)
			if err != nil {
				t.Fatal(err)
			}
			if idsOf(f.tracks) != tt.desired || !reflect.DeepEqual(f.writes, tt.methods) {
				t.Fatalf("%s %v", idsOf(f.tracks), f.writes)
			}
			if !strings.Contains(out.String(), "Applied and verified:") {
				t.Fatal(out.String())
			}
		})
	}
}

func TestSyncRejectsDuplicateRemovalBeforeAnyWrite(t *testing.T) {
	for _, tt := range []struct{ current, desired string }{{"AAB", "A"}, {"AAB", ""}, {"ABAC", "BA"}} {
		f, s := startFake(t, tt.current)
		err := applySyncPlan(context.Background(), s.Client(), s.URL, "token", "list", f.snapshot(), preparedFor(t, tt.current, tt.desired), io.Discard)
		if err == nil || !strings.Contains(err.Error(), "duplicated Spotify URI") || f.requests != 0 {
			t.Fatalf("%v requests=%d", err, f.requests)
		}
	}
}

func TestSyncConfirmation(t *testing.T) {
	for _, answer := range []string{"no\n", "\n", "YES\n", "y\n", "", "yes\n"} {
		t.Run(fmt.Sprintf("%q", answer), func(t *testing.T) {
			f, s := startFake(t, "A")
			var out, prompts bytes.Buffer
			err := syncSongs(context.Background(), s.Client(), s.URL, "token", "list", nil, strings.NewReader(answer), &prompts, &out)
			if answer == "yes\n" {
				if err != nil || len(f.writes) != 1 {
					t.Fatalf("%v %v", err, f.writes)
				}
			} else if err == nil || len(f.writes) != 0 {
				t.Fatalf("%v %v", err, f.writes)
			}
			for _, want := range []string{"Playlist: Test Playlist [ID: list]", "SYNC PREVIEW", "removes every entry"} {
				if !strings.Contains(out.String(), want) {
					t.Fatal(out.String())
				}
			}
			if !strings.Contains(prompts.String(), "Type yes") {
				t.Fatal(prompts.String())
			}
		})
	}
}

func TestSyncSharesSelectionAndConfirmationReader(t *testing.T) {
	for _, answer := range []string{"1\nyes\n", "1\nno\n"} {
		f, s := startFake(t, "")
		a := candidate("A", "Song", "Artist")
		b := a
		b.ID = "B"
		f.search = []searchTrack{a, b}
		var out, prompts bytes.Buffer
		err := syncSongs(context.Background(), s.Client(), s.URL, "token", "list", []Song{{"Artist", "Song"}}, strings.NewReader(answer), &prompts, &out)
		if strings.Contains(answer, "yes") {
			if err != nil || len(f.writes) != 1 {
				t.Fatalf("%v %s", err, prompts.String())
			}
		} else if err == nil || len(f.writes) != 0 {
			t.Fatalf("%v %v", err, f.writes)
		}
	}
}

func TestSyncNoOpAndUnresolved(t *testing.T) {
	for _, song := range []Song{{"Artist", "A"}, {"Artist", "Missing"}} {
		f, s := startFake(t, "A")
		var out, prompts bytes.Buffer
		err := syncSongs(context.Background(), s.Client(), s.URL, "token", "list", []Song{song}, strings.NewReader("yes\n"), &prompts, &out)
		if song.Title == "A" {
			if err != nil || !strings.Contains(out.String(), "Already correct") {
				t.Fatal(err, out.String())
			}
		} else if err == nil {
			t.Fatal("unresolved accepted")
		}
		if len(f.writes) != 0 || strings.Contains(prompts.String(), "Type yes") {
			t.Fatal("unexpected confirmation or writes")
		}
	}
}

func TestSyncAddUsesVerifiedSnapshot(t *testing.T) {
	for _, scenario := range []string{"success", "external edit", "unstable read", "wrong sequence"} {
		t.Run(scenario, func(t *testing.T) {
			f, s := startFake(t, "A")
			f.writeSnapshot = "write-response"
			f.metadataHook = func(f *fakePlaylist) {
				// Reads 2 and 3 bracket the first ADD's verification;
				// read 4 checks the baseline before the second ADD.
				if scenario == "external edit" && f.metadataReads == 4 ||
					scenario == "unstable read" && f.metadataReads == 3 {
					f.version++
				}
			}
			if scenario == "wrong sequence" {
				f.wrongResult = 1
			}
			var out bytes.Buffer
			err := applySyncPlan(context.Background(), s.Client(), s.URL, "token", "list", f.snapshot(), preparedFor(t, "A", "ABB"), &out)
			if scenario == "success" {
				if err != nil || idsOf(f.tracks) != "ABB" || len(f.writes) != 2 ||
					!strings.Contains(out.String(), "2 added, 0 removed, 0 moved") {
					t.Fatalf("err=%v tracks=%s writes=%v output=%s", err, idsOf(f.tracks), f.writes, out.String())
				}
				return
			}
			want := "playlist verification failed"
			if scenario == "external edit" {
				want = "before write): playlist changed: snapshot mismatch"
			} else if scenario == "unstable read" {
				want = "playlist changed while reading items"
			}
			if err == nil || !strings.Contains(err.Error(), want) || len(f.writes) != 1 {
				t.Fatalf("err=%v writes=%v", err, f.writes)
			}
		})
	}
}

func TestSyncDetectsChangedSnapshots(t *testing.T) {
	for _, when := range []int{2, 3, 6} {
		t.Run(fmt.Sprint(when), func(t *testing.T) {
			f, s := startFake(t, "AB")
			f.metadataHook = func(f *fakePlaylist) {
				if f.metadataReads == when {
					f.version++
				}
			}
			var out bytes.Buffer
			err := syncSongs(context.Background(), s.Client(), s.URL, "token", "list", nil, strings.NewReader("yes\n"), io.Discard, &out)
			if err == nil || !strings.Contains(err.Error(), "changed") {
				t.Fatal(err)
			}
			expected := 0
			if when == 6 {
				expected = 1
			}
			if len(f.writes) != expected {
				t.Fatalf("writes=%v", f.writes)
			}
		})
	}
}

func TestSyncStopsOnFailure(t *testing.T) {
	for _, tt := range []struct {
		name   string
		setup  func(*fakePlaylist)
		want   string
		writes int
	}{
		{"429", func(f *fakePlaylist) { f.failWrite = 2 }, "Retry-After: 17", 2},
		{"403", func(f *fakePlaylist) { f.failWrite = 1; f.failureStatus = 403 }, "HTTP 403", 1},
		{"500", func(f *fakePlaylist) { f.failWrite = 1; f.failureStatus = 500 }, "HTTP 500", 1},
		{"missing snapshot", func(f *fakePlaylist) { f.malformedWrite = 1 }, "write outcome unknown", 1},
		{"lost response", func(f *fakePlaylist) { f.loseResponse = 1 }, "write outcome unknown", 1},
		{"wrong result", func(f *fakePlaylist) { f.wrongResult = 1 }, "write acknowledged but outcome not verified", 1},
		{"verification API failure", func(f *fakePlaylist) { f.failVerify = true }, "HTTP 503", 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f, s := startFake(t, "ABC")
			tt.setup(f)
			var out bytes.Buffer
			err := applySyncPlan(context.Background(), s.Client(), s.URL, "token", "list", f.snapshot(), preparedFor(t, "ABC", ""), &out)
			if err == nil || !strings.Contains(err.Error(), tt.want) || len(f.writes) != tt.writes {
				t.Fatalf("%v writes=%v", err, f.writes)
			}
			if strings.Contains(out.String(), "Applied and verified") {
				t.Fatal("false success")
			}
			if tt.name == "429" && !strings.Contains(err.Error(), "0 added, 1 removed, 0 moved") {
				t.Fatal("wrong partial summary", err)
			}
		})
	}
}

func TestSyncAuthScopes(t *testing.T) {
	for _, scopes := range [][]string{nil, {publicWriteScope, privateWriteScope}} {
		u, err := url.Parse(authorizationURL(authorizeURL, "client", redirectURI, "state", "verifier", scopes...))
		if err != nil {
			t.Fatal(err)
		}
		got := strings.Fields(u.Query().Get("scope"))
		want := append([]string{readScope}, scopes...)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%v", got)
		}
	}
}

func TestPlanStillReadOnly(t *testing.T) {
	f, s := startFake(t, "AB")
	var out bytes.Buffer
	err := planSongs(context.Background(), s.Client(), s.URL, "token", "list", nil, strings.NewReader("yes\n"), io.Discard, &out)
	if err != nil || len(f.writes) != 0 || !strings.Contains(out.String(), "DRY RUN") {
		t.Fatalf("%v %v", err, f.writes)
	}
}
