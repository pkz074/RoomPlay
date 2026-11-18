# Kaira — Web Controller Frontend Owner

## Start here
Read in this order before editing code:
1. `docs/ARCHITECTURE.md` — authoritative component boundaries and failure model
2. `docs/API.md` — authoritative JSON, HTTP, SSE and NATS wire contracts
3. `docs/DEVELOPMENT.md` — repository layout, team ownership and Git workflow
4. `docs/TEST_PLAN.md` — minimum coverage and integration expectations

If the documents disagree, **stop and flag the exact conflict** to João (pkz074). Do not silently invent a new schema or change a shared contract. The proposed paths are a target layout; check the repository before assuming files exist.

## Mission
Build the web controller for independent RoomPlay rooms. Use plain HTML, CSS and vanilla JavaScript unless architecture owner approves change. Speak **only HTTP REST + SSE** to coordinator; the browser must never connect directly to NATS.

## Expected areas
`web/` (HTML, CSS, JavaScript, static assets). Coordinate with Najin on API payloads and Colton on music metadata. Do not modify coordinator or player internals to make UI work.

## Required UI
- List room cards with online/offline status, selected room, and stale/unknown state if disconnected.
- Browse licensed track catalog (`GET /api/v1/tracks`) with title, artist, duration; choose Play Now or Add to Queue.
- Selected room shows current track, position/progress, playing/paused/stopped, upcoming queue, and history display where practical.
- Prominent Previous, Play/Pause (Resume when paused), Next controls. Put Stop and Clear Queue in a secondary actions menu; never combine them into one destructive command.
- Remove individual upcoming queue items using their unique `item_id`, not track ID.
- Previous→Next returns to the original track; distinguish stopped from paused in controls.
- Disable or clearly warn when target room offline; do not claim command success before confirmed response.

## Networking behavior
- Use `POST /api/v1/rooms/{id}/commands` with typed payload (`playback.*`) and a stable unique request ID. Queue CRUD uses resource-specific HTTP endpoints and `X-Request-ID` as specified in `docs/API.md`.
- Use one `EventSource('/api/v1/events')` for named `room.state` / `room.availability` events.
- Fetch room snapshots from REST on initial load and after SSE reconnect. Reject stale `instance_id` / `version` relative to current display; server is source of truth.
- Client-side progress bar interpolation is visual only; audio engine/player is authoritative. Do not increment logical version per UI tick.
- Display `COMMAND_TIMEOUT` as **outcome unknown** and refresh state, never issue a new command with a fresh ID automatically.
- Handle structured `{request_id,error:{code,message}}` HTTP errors; clear loading indicators on error.

## Suggested PR slices
1. Accessible responsive static layout (room cards, selected player, library, queue, menu).
2. REST client wrapper + mock fixtures matching API.md; loading/empty/offline states.
3. Playback/queue buttons with unique request IDs and error notifications.
4. SSE event handling and snapshot reconnection.
5. Manual UI verification against >=2 live simulated players.

## Testing / review
- Test render of zero/one/multiple rooms, offline and stale UI, empty queues, duplicate songs with unique IDs.
- Test correct command/queue request shapes and disable repeated clicks while pending if appropriate.
- Confirm updates don't overwrite newer state with stale SSE messages.
- Check keyboard focus, button names, status readability, responsive layout.
- Record screenshots and a short demonstration in PR.

## Collaboration
Colton owns music catalog and audio file service; align tracks field names and licensing before rendering. Najin owns API; do not introduce custom endpoints as shortcuts.


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
