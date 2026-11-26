# Cross-service Integration Tests

João owns the integration test harness. Use a real Core NATS broker, coordinator, library and multiple simulated room processes.

See `docs/TEST_PLAN.md`. Test failure isolation, restart registration, timeout unknown-outcome behavior, request ID deduplication, and stale-state reconciliation.
The HTTP tests run with `go test ./...` and the frontend transport tests with
`node --test tests/integration/frontend_http.test.js`.

Run the broker suite with:

```sh
go test -race -tags=broker ./tests/integration -run '^TestBroker' -count=1 -timeout=2m
```

This suite starts pinned NATS Server v2.15.3 instances on loopback TCP ports
inside the test process and shuts them down on cleanup. No installed broker,
external service, speaker or fixed port is required. It uses the actual
coordinator registry service, NATS player and simulation backend. The `broker`
CI job runs this command separately from the ordinary unit suite.

Coverage includes command/query replies, state events and lease metadata,
request deduplication, duplicate-registration cleanup, ordinary-subscriber
fanout, heartbeat/token fencing and expiry, coordinator restart, broker
disconnect/reconnect, malformed registration, and a reply held until after
the caller's deadline despite successful execution.

These are component/service tests using real broker connections. They do not
yet exercise coordinator REST/SSE, the full browser/library path or separate
player processes; those remain P4/P5. The fanout test documents why registry
tokens alone cannot prevent arbitrary live duplicate command subscribers.
