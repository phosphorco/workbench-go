# Workbench 0.12.2

Recover context contributions after an executable provider terminates or exits.
Previously the runtime could keep leasing the closed transport, with each failed
callback refreshing its idle timer. A busy session could therefore continue
reporting `write context.contribute request: write |1: file already closed`
until a sufficiently long idle gap or configuration change.

A later callback now checks process/transport liveness, closes and reaps a dead
helper through the existing bounded process owner, and acquires a new helper.
The original failed invocation is not replayed. Confirmed source receipts stay
intact: unchanged content remains suppressed, and changed content is evaluated
from current state. A failure racing the liveness check remains an ordinary
reported failure; this is not an exactly-once delivery guarantee.

Tests cover deadline termination, unexpected process exit during a contribution,
no replay, same-state suppression, changed-state confirmation and the process
limit. An HTTP error returned by a still-running provider remains distinct from
a terminated process.

This is a binary-only patch on 0.12.1. The Pkl contract remains 0.12.0 at its
original coordinates; no declaration or timeout migration is required. Shared
hook users must adopt the new immutable executable through their normal setup.
Existing processes retain their launch commands and can bootstrap their old
runtime again after idle; upgrading a resident daemon alone is not durable
adoption by those clients. No model provider or user session is restarted by
this release.
