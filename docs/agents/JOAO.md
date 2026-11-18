# João — Architecture, Integration and Quality Lead

## Start here
Read in this order before editing code:
1. `docs/ARCHITECTURE.md` — authoritative component boundaries and failure model
2. `docs/API.md` — authoritative JSON, HTTP, SSE and NATS wire contracts
3. `docs/DEVELOPMENT.md` — repository layout, team ownership and Git workflow
4. `docs/TEST_PLAN.md` — minimum coverage and integration expectations

If the documents disagree, **stop and flag the exact conflict** to João (pkz074). Do not silently invent a new schema or change a shared contract. The proposed paths are a target layout; check the repository before assuming files exist.

## Mission
You own coherent cross-service contracts and system correctness. Review teammates' code and write integration/failure/concurrency tests; do not implement every component yourself.

## File ownership
Primary: `internal/protocol/`, `docs/`, `tests/integration/`, `.github/workflows/`, root `AGENTS.md` and repository/project tooling. Coordinate rather than unilaterally editing feature-owned packages.

## First PRs
1. Establish `go.mod` with module `github.com/pkz074/RoomPlay`, Go version agreed by team; create minimal package directories and launchable stubs without guessing business logic.
2. Implement shared Go types for Track, QueueItem, PlaybackState, command request/reply, errors, registration, heartbeat and state event from `docs/API.md`.
3. Provide validation helpers for room ID and command type/payload; write table-driven protocol tests.
4. Add small Go build/vet/test/race GitHub Actions checks and PR template.
5. Implement integration harness to start broker, coordinator, library, and multiple simulated player processes after component PRs land.

## Contract review checklist
- Command types and subjects exactly match `docs/API.md`; registration/heartbeat state isn't mistakenly considered authoritative playback.
- Same `request_id` survives HTTP → NATS; retries with same request ID cannot double-apply a retained command.
- `playback.previous` then `next` returns to original current song; stop and queue.clear preserve promised state.
- Natural completion and paused navigation obey state machine.
- No active duplicate player ID can result in two handlers executing the same command (known design hazard — test and resolve with team before demo).
- Player failure cannot stop other rooms; coordinator/broker failure interrupts control only, not already-started local playback.
- New instance/revision fencing and SSE re-sync avoid stale UI state.
- State-version semantics and monotonic position are distinct.
- Avoid claiming exactly-once across player restart/cache eviction.

## Integration testing priority
1. Independent rooms with separate queues and different tracks.
2. Kill one player and observe heartbeat expiry while another room continues.
3. Restart coordinator/broker; subscriptions and registration recover.
4. Race test concurrent same-room mutation and distinct-room commands.
5. Inject timeout/no-responder, duplicate ID, stale event and SSE reconnection.
Capture command IDs, instance IDs, state versions, timings and reproducible demo evidence.

## PR review process
Require focused scope, test results, documented contract adherence, clear error handling and logs. Resolve conflicting assumptions in `docs/API.md` before multiple features implement them differently. Prefer short reviews over a late massive merge.

## Acceptance criteria
- All components build under one Go module, pass unit/race checks.
- A scripted end-to-end demonstration works with >=2 room processes and coordinator.
- Failure/reconnect behavior is tested and honestly documented.
- Final report distinguishes Core NATS at-most-once delivery, in-memory dedup window and unknown-timeout outcomes.


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
