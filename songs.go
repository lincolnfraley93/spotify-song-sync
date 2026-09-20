package main

import (
	"bytes"
	"fmt"
	"io"
	"strings"

	"gopkg.in/yaml.v3"
)

type Song struct {
	Artist string
	Title  string
}

func parseSongs(r io.Reader) ([]Song, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("read song list: %w", err)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, nil
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	var doc yaml.Node
	if err := decoder.Decode(&doc); err == io.EOF {
		return nil, nil
	} else if err != nil {
		return nil, fmt.Errorf("invalid YAML: %w", err)
	}
	root := doc.Content[0]
	var songs []Song
	// An empty YAML document, including a blank or comment-only file, is empty.
	if !(root.Kind == yaml.ScalarNode && root.Tag == "!!null" && root.Value == "") {
		if root.Kind != yaml.SequenceNode || root.Tag != "!!seq" {
			return nil, fmt.Errorf("line %d: expected a list of songs", root.Line)
		}
		for i, entry := range root.Content {
			song, err := parseSong(entry)
			if err != nil {
				return nil, fmt.Errorf("song %d: %w", i+1, err)
			}
			songs = append(songs, song)
		}
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); err != io.EOF {
		if err != nil {
			return nil, fmt.Errorf("invalid YAML: %w", err)
		}
		return nil, fmt.Errorf("line %d: expected a single YAML document", extra.Line)
	}
	return songs, nil
}

func parseSong(entry *yaml.Node) (Song, error) {
	var song Song
	if entry.Kind != yaml.MappingNode || entry.Tag != "!!map" {
		return song, fmt.Errorf("line %d: expected an artist/title mapping", entry.Line)
	}
	seen := make(map[string]bool, 2)
	for i := 0; i < len(entry.Content); i += 2 {
		key, value := entry.Content[i], entry.Content[i+1]
		if key.Kind != yaml.ScalarNode || key.Tag != "!!str" || (key.Value != "artist" && key.Value != "title") {
			return song, fmt.Errorf("line %d: unknown field %q; expected artist or title", key.Line, key.Value)
		}
		if seen[key.Value] {
			return song, fmt.Errorf("line %d: duplicate field %q", key.Line, key.Value)
		}
		seen[key.Value] = true
		if value.Kind != yaml.ScalarNode || value.Tag != "!!str" || strings.TrimSpace(value.Value) == "" {
			return song, fmt.Errorf("line %d: %s must be a nonempty string", value.Line, key.Value)
		}
		if key.Value == "artist" {
			song.Artist = value.Value
		} else {
			song.Title = value.Value
		}
	}
	for _, field := range []string{"artist", "title"} {
		if !seen[field] {
			return song, fmt.Errorf("line %d: missing field %q", entry.Line, field)
		}
	}
	return song, nil
}
