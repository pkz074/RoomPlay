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


## Mandatory Git safety rules (all humans and AI agents)
**Never commit or push directly to `main`.** Never execute `git push origin main`, `git push --force` to `main`, delete `main`, or bypass branch protection. Do not merge a PR without the authorized human review and passing required checks. These rules apply even when the user asks to move quickly.

For every code or documentation change:
1. Start from updated `main`: `git switch main && git pull --ff-only origin main`.
2. Create or switch to your own task branch, e.g. `git switch -c feat/my-task`. **Before editing, committing, or pushing**, check `git branch --show-current`; refuse to continue if it prints `main` or an unexpected branch.
3. Stage only relevant files, commit on that task branch, and push **only that branch**: `git push -u origin <task-branch>`.
4. Open a PR with **base: main** and **head: task branch**. Request review from `pkz074` (for João's PRs, request a different teammate). Wait for CI and required approvals; do not self-approve.
5. Only merge through the protected GitHub PR flow after the authorized reviewer approves it. Do not use force pushes or API calls to bypass restrictions.
6. If GitHub rejects an operation because of protections, **stop and report the error**. Never disable protections or try a workaround without explicit authorization.

Before a write action, check branch name and intended destination. A coding assistant must explain and confirm when a requested action could target `main` directly. Read [Git workflow](../../docs/GIT_WORKFLOW.md) for examples.
