# Sandbox

`Execute(context.Context, Invocation) (Outcome, error)` runs one Linux Docker
container. The image must use `@sha256:<64 lowercase hex digits>`. Set `Role`
to `run` or `select`, `Network` to `none` or `unrestricted`, and a positive
`Timeout`. Select uses `UnitID = "*"`.

The caller stages `WorkDir` with exactly the selected inputs and `VerifierDir`
with the program, and supplies an existing writable `OutputDir`. Upstream
directories are introduced by name. Supplying `ScratchDir` grants a writable
`/scratch` mount for a weak toolchain. The caller owns retained-path filtering,
version matching, clean audits, and scratch disposal.

The package fixes the container mount destinations, read-only root, executable
tmpfs, user identity, and cleared process environment. PATH comes from the
pinned image; HOME is `/tmp`; VERIFY_UNIT and VERIFY_ROLE come from the
invocation. Stdin contains only the JSON unitId object. Read-only binds include
read-only submounts.

After the container stops, it is removed before outputs are read. Files are
opened relative to held directory descriptors with `O_NOFOLLOW | O_NONBLOCK`,
and inspected through the opened descriptor. Outputs contain sorted paths,
bytes, and Git file modes. Artifact symlinks are recorded as mode `120000`
with their target text read through `readlinkat` on the held parent directory;
neither file nor directory symlinks are followed. FIFOs, sockets, and devices
remain refused.

Report consumers must use `ReadRegularOutput(OutputDir, "report.json")`, or
the appropriate relative Report path, before decoding a Report. This helper
refuses symlinks in every path component and requires a regular leaf file.
`Execute` and `CollectOutputs` preserve artifact symlinks regardless of filename;
the engine supplies the distinction between artifacts and Reports.

Nonzero exits, deadline expiry, cancellation, Docker failures, and output
refusals return `*ToolFailure`. The outcome retains stdout, stderr, exit code
(`-1` if unavailable), and timeout evidence. Failed executions have no collected
outputs; callers must mark the Unit unknown and never attest them.

The package owns no digests, action keys, blob staging, report decoding, or
scratch policy. `testdata/generate.mts` runs the TypeScript Sandbox reference
to produce committed stdin parity fixtures, including select and escaped unit
identifiers, and uses `Store.collect` to produce artifact mode/byte/digest
fixtures. Its first argument is the absolute reference `Sandbox.mts` path.
