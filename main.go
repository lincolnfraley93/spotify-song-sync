package main

import (
	"fmt"
	"io"
	"os"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string, out io.Writer) error {
	if len(args) == 2 && args[0] == "sync" {
		return runWorkflow(args[1], out, os.Stderr, "sync")
	}
	if len(args) == 2 && args[0] == "plan" {
		return runSongWorkflow(args[1], out, os.Stderr, true)
	}
	if len(args) == 2 && args[0] == "resolve" {
		return runResolve(args[1], out, os.Stderr)
	}
	if len(args) != 1 {
		return fmt.Errorf("usage: spotify-song-sync <songs.yaml> | spotify | resolve <songs.yaml> | plan <songs.yaml> | sync <songs.yaml>")
	}
	if args[0] == "spotify" {
		return runSpotify(out, os.Stderr)
	}
	f, err := os.Open(args[0])
	if err != nil {
		return err
	}
	defer f.Close()
	songs, err := parseSongs(f)
	if err != nil {
		return fmt.Errorf("%s: %w", args[0], err)
	}
	for i, song := range songs {
		if _, err := fmt.Fprintf(out, "%d. %s — %s\n", i+1, song.Artist, song.Title); err != nil {
			return err
		}
	}
	_, err = fmt.Fprintf(out, "Total: %d songs\n", len(songs))
	return err
}
