package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

type playlistMetadata struct {
	Name     string `json:"name"`
	Snapshot string `json:"snapshot_id"`
}

func fetchPlaylistMetadata(ctx context.Context, client *http.Client, base, token, id string) (playlistMetadata, error) {
	var metadata playlistMetadata
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/playlists/"+id+"?fields=name,snapshot_id", nil)
	if err != nil {
		return metadata, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		return metadata, fmt.Errorf("read playlist snapshot: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return metadata, spotifyHTTPError("snapshot read", resp)
	}
	if err := json.NewDecoder(resp.Body).Decode(&metadata); err != nil {
		return metadata, fmt.Errorf("invalid playlist metadata: %w", err)
	}
	if strings.TrimSpace(metadata.Snapshot) == "" {
		return metadata, fmt.Errorf("playlist metadata missing snapshot_id")
	}
	return metadata, nil
}

// Bracket pagination with snapshot reads; never retry a changed read.
func fetchStablePlaylist(ctx context.Context, client *http.Client, base, token, id, expected string) (playlistMetadata, []playlistItem, error) {
	before, err := fetchPlaylistMetadata(ctx, client, base, token, id)
	if err != nil {
		return before, nil, err
	}
	if expected != "" && before.Snapshot != expected {
		return before, nil, fmt.Errorf("playlist changed: snapshot differs from expected state; run plan again")
	}
	items, err := fetchPlaylistItems(ctx, client, base, id, token)
	if err != nil {
		return before, nil, err
	}
	after, err := fetchPlaylistMetadata(ctx, client, base, token, id)
	if err != nil {
		return before, nil, err
	}
	if before.Snapshot != after.Snapshot {
		return before, nil, fmt.Errorf("playlist changed while reading items; run plan again")
	}
	return after, items, nil
}

// URI-only DELETE is safe here only when that URI occurs once. Reject duplicate
// removals even when all copies would be removed: keep execution one operation per
// displayed occurrence, without assuming undocumented DELETE semantics.
func validateSyncPlan(prepared preparedPlan) error {
	counts := make(map[string]int)
	for _, track := range prepared.Current {
		counts[track.ID]++
	}
	for _, op := range prepared.Plan.Operations {
		if op.Kind == "remove" && counts[op.Entry.Track.ID] > 1 {
			return fmt.Errorf("sync rejected before writes: removal of ID %s targets an occurrence of a duplicated Spotify URI (%d copies); the documented URI-only DELETE cannot safely target that occurrence. No playlist changes made", op.Entry.Track.ID, counts[op.Entry.Track.ID])
		}
	}
	return nil
}

func confirmSync(ctx context.Context, input *promptInput, prompts io.Writer) error {
	if _, err := fmt.Fprint(prompts, "Apply these changes to this playlist? Type yes to continue: "); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case line, ok := <-input.channel():
		if !ok || strings.TrimSpace(line) != "yes" {
			return fmt.Errorf("sync cancelled; no playlist changes made")
		}
		return nil
	}
}

func syncSongs(ctx context.Context, client *http.Client, base, token, playlistID string, songs []Song, input io.Reader, prompts, out io.Writer) error {
	metadata, items, err := fetchStablePlaylist(ctx, client, base, token, playlistID, "")
	if err != nil {
		return err
	}
	promptCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	sharedInput := &promptInput{ctx: promptCtx, reader: input}
	prepared, err := preparePlan(ctx, client, base, token, songs, items, sharedInput, prompts, out)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(out, "Playlist: %s [ID: %s]\n", metadata.Name, playlistID); err != nil {
		return err
	}
	if err := printPlanWithHeading(out, prepared.Plan, "SYNC PREVIEW — NO CHANGES UNTIL CONFIRMED"); err != nil {
		return err
	}
	if err := validateSyncPlan(prepared); err != nil {
		return err
	}
	if len(prepared.Plan.Operations) == 0 {
		return nil
	}
	if len(prepared.Desired) == 0 {
		if _, err := fmt.Fprintln(out, "This plan removes every entry from the playlist."); err != nil {
			return err
		}
	}
	if err := confirmSync(ctx, sharedInput, prompts); err != nil {
		return err
	}
	return applySyncPlan(ctx, client, base, token, playlistID, metadata.Snapshot, prepared, out)
}

// The simulation is also used to compare every successful write with readback.
func applyOperation(current []searchTrack, op planOperation) ([]searchTrack, error) {
	next := append([]searchTrack(nil), current...)
	validFrom := op.From >= 0 && op.From < len(next) && next[op.From].ID == op.Entry.Track.ID
	switch op.Kind {
	case "remove":
		if !validFrom {
			return nil, fmt.Errorf("invalid removal position/ID")
		}
		next = append(next[:op.From], next[op.From+1:]...)
	case "move":
		// buildPlan only moves later entries earlier.
		if !validFrom || op.To < 0 || op.To >= op.From {
			return nil, fmt.Errorf("invalid move position/ID")
		}
		track := next[op.From]
		copy(next[op.To+1:op.From+1], next[op.To:op.From])
		next[op.To] = track
	case "add":
		if op.To < 0 || op.To > len(next) || op.Entry.Track.ID == "" {
			return nil, fmt.Errorf("invalid addition position/ID")
		}
		next = append(next, searchTrack{})
		copy(next[op.To+1:], next[op.To:len(next)-1])
		next[op.To] = op.Entry.Track
	default:
		return nil, fmt.Errorf("unknown operation %q", op.Kind)
	}
	return next, nil
}

func writePlaylistOperation(ctx context.Context, client *http.Client, base, token, id, snapshot string, op planOperation) (string, error) {
	uri := "spotify:track:" + op.Entry.Track.ID
	var method string
	var body any
	success := http.StatusOK
	switch op.Kind {
	case "remove":
		method = http.MethodDelete
		body = struct {
			Items    []map[string]string `json:"items"`
			Snapshot string              `json:"snapshot_id"`
		}{[]map[string]string{{"uri": uri}}, snapshot}
	case "move":
		method = http.MethodPut
		body = struct {
			Start    int    `json:"range_start"`
			Before   int    `json:"insert_before"`
			Length   int    `json:"range_length"`
			Snapshot string `json:"snapshot_id"`
		}{op.From, op.To, 1, snapshot}
	case "add":
		method = http.MethodPost
		success = http.StatusCreated
		body = struct {
			URIs     []string `json:"uris"`
			Position int      `json:"position"`
		}{[]string{uri}, op.To}
	default:
		return "", fmt.Errorf("unsupported operation %q", op.Kind)
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, method, base+"/playlists/"+id+"/items", bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	// Disable body replay; a lost response must never trigger a silent write retry.
	req.GetBody = nil
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("write outcome unknown (request may have been applied): %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != success {
		return "", fmt.Errorf("%w; operation not confirmed, server-side outcome may be unknown", spotifyHTTPError(op.Kind, resp))
	}
	var result struct {
		Snapshot string `json:"snapshot_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil || strings.TrimSpace(result.Snapshot) == "" {
		return "", fmt.Errorf("write outcome unknown: Spotify returned success without a usable snapshot_id; operation may have been applied")
	}
	return result.Snapshot, nil
}

func verifySyncState(ctx context.Context, client *http.Client, base, token, id string, expected []searchTrack) (string, error) {
	// Verify the actual sequence under a stable read snapshot. The mutation's
	// response snapshot need not equal the snapshot observed by this read.
	metadata, items, err := fetchStablePlaylist(ctx, client, base, token, id, "")
	if err != nil {
		return "", err
	}
	if len(items) != len(expected) {
		return "", fmt.Errorf("playlist verification failed: item count differs from expected state")
	}
	for i, item := range items {
		if item.Item == nil || item.IsLocal || item.Item.IsLocal || item.Item.Type != "track" || item.Item.ID != expected[i].ID {
			return "", fmt.Errorf("playlist verification failed: track at position %d differs from expected state", i+1)
		}
	}
	return metadata.Snapshot, nil
}

func applySyncPlan(ctx context.Context, client *http.Client, base, token, id, snapshot string, prepared preparedPlan, out io.Writer) error {
	if err := validateSyncPlan(prepared); err != nil {
		return err
	}
	expected := prepared.Current
	counts := map[string]int{}
	stop := func(step int, phase string, err error) error {
		return fmt.Errorf("sync stopped at step %d (%s): %w. Verified applied: %d added, %d removed, %d moved. No retry, recomputation, or rollback performed", step, phase, err, counts["add"], counts["remove"], counts["move"])
	}
	for i, op := range prepared.Plan.Operations {
		next, err := applyOperation(expected, op)
		if err != nil {
			return stop(i+1, "before write", err)
		}
		// Also covers edits during resolution/confirmation and between operations.
		metadata, err := fetchPlaylistMetadata(ctx, client, base, token, id)
		if err != nil {
			return stop(i+1, "before write", err)
		}
		if metadata.Snapshot != snapshot {
			return stop(i+1, "before write", fmt.Errorf("playlist changed: snapshot mismatch; run plan again"))
		}
		newSnapshot, err := writePlaylistOperation(ctx, client, base, token, id, snapshot, op)
		if err != nil {
			return stop(i+1, op.Kind, err)
		}
		// Adopt the observed snapshot only after the expected sequence is verified.
		newSnapshot, err = verifySyncState(ctx, client, base, token, id, next)
		if err != nil {
			return stop(i+1, "write acknowledged but outcome not verified", err)
		}
		counts[op.Kind]++
		expected = next
		snapshot = newSnapshot
	}
	// Last write's readback is the final verification, not just a success status.
	if len(expected) != len(prepared.Desired) {
		return stop(len(prepared.Plan.Operations), "final verification", fmt.Errorf("desired length mismatch"))
	}
	for i, track := range expected {
		if track.ID != prepared.Desired[i].ID {
			return stop(len(prepared.Plan.Operations), "final verification", fmt.Errorf("desired sequence mismatch"))
		}
	}
	_, err := fmt.Fprintf(out, "\nApplied and verified: %d added, %d removed, %d moved; %d tracks in desired order.\n", counts["add"], counts["remove"], counts["move"], len(expected))
	return err
}
