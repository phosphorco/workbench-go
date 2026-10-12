# Workbench 0.9.1

Workbench 0.9.1 updates `workbench verify` with grouped, parameterized diagnostic
output. Diagnostic templates and parameters separate shared wording from the
values that differ between occurrences. The help starts with the primary
`--gate` and `--json` options.

This is a binary-only release. The Pkl contract stays at 0.9.0, with the same
package metadata, ZIP, checksums, and original 0.9.0 download coordinates.
No verifier declaration or resource-contract version changes are required.

Install with `mise use github:phosphorco/workbench-go@0.9.1`. The release carries
four macOS/Linux ARM64/x64 archives with private pinned runtimes, checksums and
attestations, alongside the unchanged `workbench@0.9.0` metadata and ZIP.
See [Verification](verification.md) for commands and declaration contracts.
