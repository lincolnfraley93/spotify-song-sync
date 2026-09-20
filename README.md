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
Playlist writes, song matching, comparison, and synchronization are not implemented.

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
