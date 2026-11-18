# Cross-service Integration Tests

João owns the integration test harness. Use a real Core NATS broker, coordinator, library and multiple simulated room processes.

See `docs/TEST_PLAN.md`. Test failure isolation, restart registration, timeout unknown-outcome behavior, request ID deduplication, and stale-state reconciliation.
The integration harness has not been implemented.
