Given a local song list, synchronize a specific Spotify playlist to match it.

The CLI validates a local YAML song list and can read a configured Spotify playlist.

```yaml
- artist: Fleetwood Mac
  title: Dreams
- artist: Prince
  title: '1999'
```

Run with Go 1.23.4 or newer:

```sh
go run . songs.yaml
```

The command prints each song in order and the total count. Duplicates and the
original artist/title text are preserved. Both `[]` and a blank file are valid
empty lists; comment-only files are also empty.

Use a single YAML document containing a list of mappings with exactly `artist`
and `title`. Both values must be nonempty strings, not just whitespace. Quote
numeric titles such as `'1999'`. Unknown, duplicate, or missing fields and
non-string values are rejected. Errors identify the file, song, field, and YAML
line where applicable; the command exits unsuccessfully without printing a
partial list. YAML aliases and merge keys are not supported.

Run tests with `go test ./...`.

## Read a Spotify playlist

Create an application in the Spotify Developer Dashboard. For development-mode
apps, the app owner needs Spotify Premium and the signed-in user must be allowed
in the app's user management. Use a playlist you own or collaborate on.
Register this exact redirect URI:

```
http://127.0.0.1:8888/callback
```

Set the app client ID and the playlist ID (the ID portion of its Spotify URL):

```sh
export SPOTIFY_CLIENT_ID='your-client-id'
export SPOTIFY_PLAYLIST_ID='your-playlist-id'
go run . spotify
```

The CLI opens the authorization page in your default browser automatically. If
that fails, open the printed URL manually. The CLI uses PKCE and only
requests `playlist-read-private`. It needs no client secret. Port 8888 must be
available. Authorization times out after five minutes; Ctrl-C cancels the command.
Tokens stay in memory, so each invocation requires browser authorization. No
credentials are saved, and refresh tokens are not used in this milestone.

The command fetches all pages before printing playlist entries in order, including
duplicates, followed by an item count. Local tracks, unavailable entries, and
non-song items are labeled explicitly. Login instructions go to stderr and the
playlist goes to stdout. Each HTTP request has a 30-second timeout. Errors exit
unsuccessfully; HTTP 429 errors include `Retry-After` when supplied, with no retries.
Playlist writes, comparison, and synchronization are not implemented.

### Save configuration between terminal sessions

Fill in `spotify.yaml` in the project directory once:

```yaml
client_id: "your-client-id"
playlist_id: "your-playlist-id"
```

Then run `go run . spotify` from that directory. This local file is ignored by
Git. Nonempty `SPOTIFY_CLIENT_ID` and `SPOTIFY_PLAYLIST_ID` environment variables
override the corresponding file values. Browser authorization is still required
on each run; this file stores configuration, not access or refresh tokens.

## Resolve YAML songs to Spotify tracks

```sh
go run . resolve songs.yaml
```

This read-only preview validates the file, authenticates once, and fetches the
entire configured playlist before searching. Both `client_id` and `playlist_id`
are required in `spotify.yaml` (or their existing environment variables). Empty
input prints an empty summary without configuration, authentication, or requests.

For each song, an exact normalized artist/title match in the playlist takes
precedence: the first matching track in playlist order supplies its exact Spotify
ID, with no search. Local files, non-track entries, and entries without usable
IDs or metadata cannot supply a playlist choice.

Comparison ignores case, trims/collapses whitespace, maps curly apostrophes to
straight apostrophes, and maps the ellipsis character to three periods. Other
punctuation, accents, and version labels remain significant. Artist matching accepts a single credited artist or semicolon-separated names
that must all appear among the credited artists (ignoring order and surrounding
whitespace). Whole-string artist matches remain supported; commas, ampersands,
and `feat.` are not split. Original input text is retained for display.

Songs not represented in the playlist use the existing strict search and ISRC
rules over the first ten candidates. A confident result selects the first track
in the accepted group in search order as its representative, without album-name
or release-quality preferences. The report retains the group and its reasoning.

Ambiguous searches display numbered exact candidates with artists, title, album,
ISRC, and Spotify links. When there is no exact match, nonmatching search
candidates are offered for explicit manual selection. If the filtered search
returns no candidates, one free-text artist/title search supplies up to ten manual
options; these results are never automatically accepted. No results leaves the
song unmatched. At each prompt, enter a listed number, `s` to skip, or `q` to quit.
Invalid input prompts again. Quit, EOF, or Ctrl-C cancels with a nonzero exit.

The report identifies each selected ID and its source: `playlist`, `search`, or
`manual`. Matched songs appear first, followed by labeled unmatched and ambiguous
sections. Original input numbers and relative order within each section are
preserved, and the overall summary is last. Skipped songs remain unresolved.
Duplicate normalized inputs reuse the same decision within the run while retaining
all their positions. Decisions, including skips, are not written to disk.

Existing playlist entries act as persistent choices whenever normalized metadata
matches. New manual choices last only for this run: the preview changes neither
the playlist nor the YAML and creates no resolutions file. Even after future
playlist writes, a manually chosen track with materially different artist/title
metadata will not automatically match the original YAML entry on another run.

The full report prints after all resolution steps succeed. Unresolved/skipped
songs are normal report outcomes (exit success). API errors stop the command with
a nonzero exit and no partial report; HTTP 429 includes `Retry-After` when supplied,
without retries. No playlist writes, add/remove/reorder calculations, or
synchronization are performed.

### Conservative ISRC dominance

After strict artist/title filtering and track-ID deduplication, a recording group
may also match when all candidates have valid ISRCs, exactly one group contains
at least two distinct track IDs and more than half of the qualifying candidates,
and every competing group is a singleton. Thus 4–1, 2–1, and 3–1–1 match;
2–2, 1–1, 2–1–1, and 4–2 remain ambiguous. Unknown ISRCs prevent dominance.

The report labels this as a dominance-based heuristic and separates the selected
group from competing candidates. All candidates remain visible; the first track in the accepted group is used
as its representative, without a release-quality preference. Release counts within the first ten search results are not
independent evidence or proof of the intended recording. Album names are never
used to decide dominance. Single-track and unanimous-ISRC matches are unchanged.

## Dry-run synchronization plan

```sh
go run . plan songs.yaml
```

This command uses the same configuration and interactive resolution flow as
`resolve`, then prints the operations needed to make the fetched playlist match
the desired sequence of selected Spotify track IDs. It makes no Spotify playlist
writes. Resolution and planning share one complete playlist fetch.

Duplicates are separate occurrences. The planner retains the earliest current
occurrences up to the desired count, removes surplus occurrences from right to
left, then walks desired positions left to right. It keeps a correct occurrence,
moves the earliest later occurrence of the needed ID, or adds that ID if absent.
The final simulated sequence is checked against the desired sequence. Required
additions/removals are minimal; the number of moves is not necessarily minimal.

Operations are numbered and use 1-based positions in the simulated playlist after
preceding operations. Removed/moved entries also show original positions.
Retained entries need no explicit move but can shift through other operations;
they show original and desired positions. Every entry includes its Spotify ID.
Different IDs remain different even when they share an ISRC. The summary separates
retained, added, removed, and moved occurrences.

Unlike `resolve`, an empty song list still fetches the current playlist and plans
removal of every occurrence. Identical states report that no changes are needed.
Skipped/unresolved songs block the plan and are listed with a nonzero exit;
they never silently become removals. Local files, non-track entries, null items,
and missing track IDs in the current playlist also block planning, identifying
their position. API failures stop planning without a partial change plan.

This is a preview of the fetched state, which can become stale if the playlist is
edited elsewhere. No plan or resolution file is saved. Playlist execution and
write scopes remain out of scope.
