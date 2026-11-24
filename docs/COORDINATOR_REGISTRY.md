# Coordinator Registry: Duplicate Live Subscriber Safety

## Problem
The registry rejects a second *registration* for a live room ID (DUPLICATE_ROOM_ID)
and fences old instances using registration tokens. But NATS does not know about
registrations. Any process can subscribe to `roomplay.rooms.{id}.command`. If two
processes subscribe (e.g. a stale player that never exited plus a replacement), NATS
delivers each request to only ONE of them, so the coordinator cannot tell which one
will receive a command. Tokens protect the coordinator's registry, not the subject.

## What this PR does
- Rejects a second live instance at registration time.
- Ignores heartbeats with a mismatched instance ID or token.
- Replaces an instance only if the old one is offline/expired.
- Late heartbeats for an offline room are rejected; the player must re-register.

## Proposed solution (needs João's approval: touches the API contract)
1. Add `instance_id` to every command/get_state reply.
2. The coordinator checks that the reply's `instance_id` equals the registered one.
   A mismatch marks the room CONFLICTED: commands are refused with a 409/503 error
   instead of being routed, and the event is logged/published.
3. On every registration or renewal, the coordinator sends a `get_state` probe.
   A different instance answering reveals a duplicate quickly.
4. Players should exit if they receive a registration rejection (DUPLICATE_ROOM_ID).
5. Ambiguous routing stays blocked until the duplicate is shut down.

## Limits
Detection is by observation: a hidden duplicate that never receives a request stays
invisible until it does. Full prevention would need per-instance command subjects or
broker-side permissions, which are out of scope for the MVP.