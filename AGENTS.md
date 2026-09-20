# Project Instructions

## Development
- This is a Go CLI application.
- Prefer small, incremental changes over large rewrites.
- Keep dependencies minimal unless a dependency clearly improves the implementation.
- Preserve existing CLI behavior unless the task explicitly changes it.

## Testing
- Run `go test ./...` after implementation changes.
- Add or update tests for new behavior and failure cases.

## Spotify
- Never log or commit access tokens or secrets.
- Keep Spotify integration changes narrowly scoped to the requested milestone.

## Workflow
- Before implementing a substantial feature, inspect the repository and propose a plan first.
- Do not expand the scope beyond the approved plan without calling it out.
