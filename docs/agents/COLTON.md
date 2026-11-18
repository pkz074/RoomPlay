# Colton — Music Library and Catalog Owner

## Start here
Read in this order before editing code:
1. `docs/ARCHITECTURE.md` — authoritative component boundaries and failure model
2. `docs/API.md` — authoritative JSON, HTTP, SSE and NATS wire contracts
3. `docs/DEVELOPMENT.md` — repository layout, team ownership and Git workflow
4. `docs/TEST_PLAN.md` — minimum coverage and integration expectations

If the documents disagree, **stop and flag the exact conflict** to João (pkz074). Do not silently invent a new schema or change a shared contract. The proposed paths are a target layout; check the repository before assuming files exist.

## Mission
Build the lightweight Go HTTP library service that provides track metadata and legally reusable MP3 audio to independent players. Coordinate closely with Kaira (frontend browsing) and Kyle (player fetching). This is a separate service from coordinator and player.

## Expected areas
`cmd/library/`, `internal/library/`, `assets/music/` and catalog/license manifest. Share protocol Track type from João's `internal/protocol/`.

## Library contract
- `GET /tracks` returns `{"tracks":[{"id","title","artist","duration_ms","url"},...]}`.
- `GET /audio/{safe-file-name}` streams a catalog-approved MP3 with correct Content-Type, HTTP errors, and no arbitrary file access.
- Coordinator's public `GET /api/v1/tracks` may proxy/fetch this metadata, but coordinator must not stream MP3 bytes itself.
- Audio URL must be reachable by *players*, including separate computers. Do not embed a hardcoded localhost address as the permanent public URL.
- Each track has a stable ID and metadata. Include track source, copyright/license permission and attribution details in catalog manifest or `assets/music/ATTRIBUTION.md`.
- Prefer CC0/public-domain or explicitly redistributable demo audio. 'No copyright' label alone is not evidence of permission. Do not download, commit, or redistribute songs before checking the license.
- Validate file names/allowlist and path traversal; bound request sizes and handle missing/corrupt assets cleanly.

## Suggested PR slices
1. Catalog schema, sample metadata and license/attribution plan.
2. HTTP GET `/tracks` + unit tests for malformed/empty/duplicate catalog records.
3. Audio HTTP GET for allowlisted files + content-type, missing file, traversal tests.
4. Reachable URL configuration and MP3 test with player on a separate process.
5. Coordinate API/UI catalog integration with Kaira and Najin.

## Testing
- Catalog metadata stable and IDs unique, duration_ms nonnegative.
- Known licensed MP3 returns bytes and appropriate headers; missing files return 404.
- Requests like `../` or URL-encoded path escapes cannot read outside library root.
- Concurrent requests are safe; no shared-state race.
- Service responds with explicit failures rather than inventing track URLs.
- Document local startup and sample track provenance in PR.

## Out of scope
No playlist persistence, database, user uploads, third-party streaming service integrations, automatic internet downloading, or audio streaming through NATS.
