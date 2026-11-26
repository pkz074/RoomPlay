# Coordinator Registry: Duplicate Live Subscriber Safety

## Problem
The registry rejects a second *registration* for a live room ID (DUPLICATE_ROOM_ID)
and fences old instances using registration tokens. But NATS does not know about
registrations. Any process can subscribe to `roomplay.rooms.{id}.command`. If two
processes subscribe (e.g. a stale player that never exited plus a replacement), NATS
delivers each request to BOTH ordinary subscribers; a request/reply caller accepts
the first reply. Tokens protect the coordinator's registry, not the subject.

## What this PR does
- Rejects a second live instance at registration time.
- Ignores heartbeats with a mismatched instance ID or token.
- Replaces an instance only if the old one is offline/expired.
- Late heartbeats for an offline room are rejected; the player must re-register.

## Player cleanup and broker evidence (P3)
The player now exits and removes its room subscriptions when initial registration
or renewal returns DUPLICATE_ROOM_ID. Broker-backed tests verify that cleanup and
that the legitimate player remains controllable. A separate test demonstrates
that two ordinary subscribers both receive the same request.

This cleanup does not fence an arbitrary stale subscriber before rejection is
observed. Reply identity checks can detect some conflicts only after execution;
they cannot undo duplicate mutations. Do not enable ambiguous command routing on
the assumption that registration tokens or first-reply selection prevent it.

## P4 approved instance-targeting implementation
Commands and queries carry the current target instance and registration token.
A mismatched player ignores them before execution and does not respond. Replies
carry their accepting instance and token, which the coordinator verifies against
both the request and the current registry. Unexpected replies or a registration
change return SERVICE_UNAVAILABLE/503, requiring a fresh snapshot. No new request
ID is generated for retry. See API.md for the exact fields.

The coordinator also queries state after registration/renewal, and players publish
their current snapshot after accepting a lease. Broker-backed HTTP tests keep an
old and a replacement player subscribed simultaneously and verify that only the
targeted instance mutates. This supersedes the earlier reply-only proposal, which
could detect conflicts after a duplicate mutation but could not prevent them.

## Limits
This relies on players implementing the approved target checks. Arbitrary legacy
or malicious subscribers require shutdown or broker permissions. Tokens are not
credentials; authentication for untrusted networks remains outside the MVP.
