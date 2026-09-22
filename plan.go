package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Positions are zero-based internally; original distinguishes duplicate occurrences.
type planEntry struct {
	Track    searchTrack
	Original int
}
type planOperation struct {
	Kind     string
	Entry    planEntry
	From, To int
}
type retainedEntry struct {
	Entry planEntry
	To    int
}
type syncPlan struct {
	Operations []planOperation
	Retained   []retainedEntry
}

func buildPlan(current, desired []searchTrack) (syncPlan, error) {
	var plan syncPlan
	counts := make(map[string]int)
	for _, track := range desired {
		if track.ID == "" {
			return plan, fmt.Errorf("desired track is missing its Spotify ID")
		}
		counts[track.ID]++
	}
	work := make([]planEntry, len(current))
	surplus := make([]bool, len(current))
	for i, track := range current {
		if track.ID == "" {
			return plan, fmt.Errorf("current position %d is missing its Spotify ID", i+1)
		}
		work[i] = planEntry{Track: track, Original: i}
		if counts[track.ID] > 0 {
			counts[track.ID]--
		} else {
			surplus[i] = true
		}
	}
	for i := len(work) - 1; i >= 0; i-- {
		if !surplus[i] {
			continue
		}
		plan.Operations = append(plan.Operations, planOperation{Kind: "remove", Entry: work[i], From: i, To: -1})
		work = append(work[:i], work[i+1:]...)
	}
	for i, track := range desired {
		if i < len(work) && work[i].Track.ID == track.ID {
			plan.Retained = append(plan.Retained, retainedEntry{Entry: work[i], To: i})
			continue
		}
		found := -1
		for j := i + 1; j < len(work); j++ {
			if work[j].Track.ID == track.ID {
				found = j
				break
			}
		}
		if found >= 0 {
			entry := work[found]
			plan.Operations = append(plan.Operations, planOperation{Kind: "move", Entry: entry, From: found, To: i})
			copy(work[i+1:found+1], work[i:found])
			work[i] = entry
		} else {
			entry := planEntry{Track: track, Original: -1}
			plan.Operations = append(plan.Operations, planOperation{Kind: "add", Entry: entry, From: -1, To: i})
			work = append(work, planEntry{})
			copy(work[i+1:], work[i:len(work)-1])
			work[i] = entry
		}
	}
	if len(work) != len(desired) {
		return syncPlan{}, fmt.Errorf("internal error: plan length does not match desired state")
	}
	for i, entry := range work {
		if entry.Track.ID != desired[i].ID {
			return syncPlan{}, fmt.Errorf("internal error: plan differs at position %d", i+1)
		}
	}
	return plan, nil
}

func planTrackLabel(track searchTrack) string {
	var artists []string
	for _, artist := range track.Artists {
		artists = append(artists, artist.Name)
	}
	return fmt.Sprintf("%s — %s", strings.Join(artists, ", "), track.Name)
}

func printPlan(out io.Writer, plan syncPlan) error {
	var report strings.Builder
	fmt.Fprintln(&report, "DRY RUN — NO PLAYLIST CHANGES")
	fmt.Fprintln(&report, "============================")
	fmt.Fprintln(&report, "\nPositions start at 1. Each step uses the playlist after earlier steps.")
	counts := make(map[string]int)
	if len(plan.Operations) == 0 {
		fmt.Fprintln(&report, "\nAlready correct—no changes needed.")
	} else {
		fmt.Fprintf(&report, "\nPROPOSED CHANGES (%d)\n--------------------\n", len(plan.Operations))
	}
	for i, op := range plan.Operations {
		counts[op.Kind]++
		fmt.Fprintf(&report, "\n  %d. %s %s\n", i+1, strings.ToUpper(op.Kind), planTrackLabel(op.Entry.Track))
		switch op.Kind {
		case "remove":
			fmt.Fprintf(&report, "     Position: %d → removed (original position %d)\n", op.From+1, op.Entry.Original+1)
		case "move":
			fmt.Fprintf(&report, "     Position: %d → %d (original position %d)\n", op.From+1, op.To+1, op.Entry.Original+1)
		case "add":
			fmt.Fprintf(&report, "     Position: insert at %d\n", op.To+1)
		}
		fmt.Fprintf(&report, "     Track ID: %s\n", op.Entry.Track.ID)
	}
	if len(plan.Retained) > 0 {
		fmt.Fprintf(&report, "\nRETAINED (%d) — NO EXPLICIT MOVE NEEDED\n-------------------------------------\n", len(plan.Retained))
		for _, entry := range plan.Retained {
			fmt.Fprintf(&report, "\n  %s\n", planTrackLabel(entry.Entry.Track))
			fmt.Fprintf(&report, "     Position: original %d → desired %d\n", entry.Entry.Original+1, entry.To+1)
			fmt.Fprintf(&report, "     Track ID: %s\n", entry.Entry.Track.ID)
		}
	}
	fmt.Fprintln(&report, "\n------------------------------------------------------------")
	fmt.Fprintf(&report, "Summary: %d retained, %d added, %d removed, %d moved\n", len(plan.Retained), counts["add"], counts["remove"], counts["move"])
	_, err := io.WriteString(out, report.String())
	return err
}

func planSongs(ctx context.Context, client *http.Client, base, token, playlistID string, songs []Song, input io.Reader, prompts, out io.Writer) error {
	items, err := fetchPlaylistItems(ctx, client, base, playlistID, token)
	if err != nil {
		return err
	}
	current := make([]searchTrack, 0, len(items))
	for i, item := range items {
		if item.IsLocal || item.Item == nil || item.Item.IsLocal || item.Item.Type != "track" || item.Item.ID == "" {
			return fmt.Errorf("plan incomplete: unsupported playlist entry at position %d (local, non-track, unavailable, or missing ID); no change plan generated", i+1)
		}
		current = append(current, *item.Item)
	}
	results, err := resolveAgainstPlaylist(ctx, client, base, token, songs, items, input, prompts)
	if err != nil {
		return err
	}
	desired := make([]searchTrack, 0, len(results))
	var unresolved strings.Builder
	for i, result := range results {
		if result.Selected == nil || result.Selected.ID == "" {
			fmt.Fprintf(&unresolved, "%d. %s — %s: %s (%s)\n", i+1, result.Song.Artist, result.Song.Title, result.Status, result.Reason)
		} else {
			desired = append(desired, *result.Selected)
		}
	}
	if unresolved.Len() > 0 {
		if _, err := fmt.Fprintf(out, "UNRESOLVED SONGS — PLAN INCOMPLETE\n%s", unresolved.String()); err != nil {
			return err
		}
		return fmt.Errorf("plan incomplete—no change plan generated; resolve every input song first")
	}
	plan, err := buildPlan(current, desired)
	if err != nil {
		return err
	}
	return printPlan(out, plan)
}
