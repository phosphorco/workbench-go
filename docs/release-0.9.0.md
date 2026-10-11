# Workbench 0.9.0

Workbench 0.9.0 introduces `workbench verify`: hermetic verification of tracked
Git Trees, shared content-addressed action reuse, and session-local diagnostic
Deltas. Focus shapes agent attention; declared Gates enforce policy over the
whole Observation. Exit codes distinguish clean (`0`), blocking (`1`), invalid
or failed invocation (`2`), and incomplete Observation (`3`).

The 0.9.0 Pkl contract adds `Verifier.pkl`, `Focus.pkl`, and Repository
`verifiers`, `gates`, and `focuses`. Verifiers declare pinned container images,
input selection, upstream artifacts, Reports or artifact outputs, and optional
audited Scratch. Declaration evaluation uses frozen tracked bytes and locked,
previously acquired packages without network or ambient configuration.

`workbench verify promote` validates and installs an attested artifact under a
declared distribution contract. The former Buildable manifest-checking command
is now `workbench buildable attest`, with the same options and behavior. There
is no `buildable verify` compatibility shim in this repository.

The engine is composed of Tree capture, declaration evaluation, selectors,
sandbox execution, CAS/action storage, Report decoding, Observation folding,
session/Focus presentation, and artifact distribution packages. Docker provides
execution isolation; declared networking remains explicit. This release does
not provide a remote execution service or a macOS verification pool.

Install with `mise use github:phosphorco/workbench-go@0.9.0`. The release includes
four macOS/Linux ARM64/x64 archives with private pinned runtimes, checksums and
attestations, plus `workbench@0.9.0` package metadata and `workbench@0.9.0.zip`.
See [Verification](verification.md) for usage and declarations.
