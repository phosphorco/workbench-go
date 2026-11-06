# Workbench 0.11.0

Workbench 0.11.0 adds actor identification, assignment, and structured stalls to
`workbench plan`, with a matching 0.11.0 Pkl contract.

- Import reusable actor kinds for threads, people, and external services.
  Recognize canonical IDs and aliases, infer identity from configured environment
  variables, and use the same identity for attribution and `@me`.
- Record assignees and explicit work status with `plan update`. Optional expected
  revisions protect updates based on stale state; assignment does not dispatch workers.
- Track current stalls with a reason, release condition, and optional actor or
  plan-need reference. Broken references produce scoped warnings, and resuming
  work remains explicit.
- Keep completion evidence-derived. Configured review and decision owners bind
  acceptance fingerprints to resolved identities while ledger history stays intact.
- Export ready-made actor definitions and concise planning guidance with the
  `workbench-plan` skill.

New work-update events require a reader with coordination support. Older ledger
events remain readable; preserve a copy when comparing older readers.

This release also retains 0.10.0's producer preparation and opt-in observed build
input binding, along with historical resource-contract compatibility.

Install with `mise use github:phosphorco/workbench-go@0.11.0`. Release archives
include private pinned runtimes, licenses, checksums, and attestations for
macOS/Linux on ARM64/x64, plus the `workbench@0.11.0` Pkl package.
