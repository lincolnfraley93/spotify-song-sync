package main

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseSongs(t *testing.T) {
	for _, input := range []string{"", " \n\t\n", "[]", "# songs go here\n", "---\n"} {
		t.Run("empty_"+input, func(t *testing.T) {
			songs, err := parseSongs(strings.NewReader(input))
			if err != nil || len(songs) != 0 {
				t.Fatalf("got %v, %v; want empty list", songs, err)
			}
		})
	}
	input := "- artist: ' Beyoncé '\n  title: Halo\n- title: '1999'\n  artist: Prince\n- artist: ' Beyoncé '\n  title: Halo\n"
	want := []Song{{" Beyoncé ", "Halo"}, {"Prince", "1999"}, {" Beyoncé ", "Halo"}}
	got, err := parseSongs(strings.NewReader(input))
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, %v; want %#v", got, err, want)
	}
}

func TestParseSongsRejectsMalformedInput(t *testing.T) {
	tests := []struct {
		name, input, message string
	}{
		{"syntax", "- artist: [", "invalid YAML:"},
		{"mapping root", "artist: Prince\ntitle: Purple Rain", "line 1: expected a list"},
		{"null root", "null", "line 1: expected a list"},
		{"scalar entry", "- Prince", "song 1: line 1: expected an artist/title mapping"},
		{"null entry", "-", "song 1: line 1:"},
		{"missing artist", "- title: Halo", "song 1: line 1: missing field \"artist\""},
		{"missing title", "- artist: Beyoncé", "song 1: line 1: missing field \"title\""},
		{"unknown field", "- artist: Prince\n  title: Purple Rain\n  album: Purple Rain", "song 1: line 3: unknown field \"album\""},
		{"duplicate field", "- artist: Prince\n  title: Purple Rain\n  title: 1999", "song 1: line 3: duplicate field \"title\""},
		{"empty string", "- artist: ''\n  title: Halo", "song 1: line 1: artist must be a nonempty string"},
		{"whitespace", "- artist: Prince\n  title: '  '", "song 1: line 2: title must be a nonempty string"},
		{"null value", "- artist: Prince\n  title:", "song 1: line 2: title must be a nonempty string"},
		{"number", "- artist: Prince\n  title: 1999", "song 1: line 2: title must be a nonempty string"},
		{"boolean", "- artist: true\n  title: Halo", "song 1: line 1: artist must be a nonempty string"},
		{"sequence value", "- artist: [Prince]\n  title: Halo", "song 1: line 1: artist must be a nonempty string"},
		{"mapping value", "- artist: Prince\n  title: {name: Halo}", "song 1: line 2: title must be a nonempty string"},
		{"second song", "- artist: Prince\n  title: Purple Rain\n- artist: Beyoncé", "song 2: line 3: missing field \"title\""},
		{"multiple documents", "[]\n---\n[]", "expected a single YAML document"},
		{"invalid trailing document", "[]\n---\n[", "invalid YAML:"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			songs, err := parseSongs(strings.NewReader(tt.input))
			if err == nil || !strings.Contains(err.Error(), tt.message) {
				t.Fatalf("got %v; want error containing %q", err, tt.message)
			}
			if len(songs) != 0 {
				t.Fatalf("returned partial songs: %v", songs)
			}
		})
	}
}
