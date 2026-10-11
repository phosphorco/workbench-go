# Verification engine

`Evaluate(ctx, tree, home)` takes declaration, root PklProject, and committed package-lock bytes
from the captured Tree and delegates evaluation to `declaration`.
`Observe(ctx, Options)` orders registered Verifiers through that package,
stores snapshot blobs, resolves upstream artifact digests, selects every Unit,
executes cache misses in a bounded pool, and returns `observe.Fold` over all
selected Units and explicit unknowns.

`Options.Random` supplies the Scratch audit source. It is sampled serially
within one Observe call, only after a warm Run has succeeded and a matching
Scratch pointer was restored. Zero/default Concurrency uses four workers.
Caller-supplied declarations and snapshot data are read-only.

Own selection uses `selector.Own` and caches its exact stdout bytes in the
single action index as `{"select.json": digest}`. Selection bytes are staged in
an engine-owned regular file and checked with `sandbox.ReadRegularOutput`
before the engine decodes them. A Verifier's `/out/select.json` has no authority
to supply selection. Cached selections are validated against the current Tree.

Inputs, normalized program paths, and upstream artifacts are materialized from
CAS into fresh directories under `home/exec`. `/work` contains exactly the
selected inputs. Program paths are relative to their longest common directory.
The sandbox fixes container mounts, environment, stdin, network, and timeout.
Every decoded Report passes through `sandbox.ReadRegularOutput`; only the
Report decoder's normalized `Decoded.Bytes` are stored. A refused, missing, or
symlink Report is unknown, including on a cache hit. Artifact collection keeps
only declared outputs, preserving symlink text and executable modes.

Nonzero exits publish failure evidence through `store.Publish`, which refuses
Attestation. Other execution failures also retain immutable witnesses. A
failed artifact makes dependent Verifiers wholly unknown. Successful
publication returns the first stored Attestation, with recomputed/carried
provenance reflecting whether this caller won.

Weak Scratch is restored, filtered, saved, and dropped through `store`. Audits
compare output digests before publication. A mismatch writes a `scratch-audit`
witness, drops the version's Scratch, and returns unknown without publishing.
Witnessed versions cannot restore or save Scratch.

`Main(ctx, argv, IO)` supplies the top-level CLI used by `workbench verify`.
It delegates grammar, Focus, Gate, Last Seen, Delta, presentation, and exit
policy to `client`. All Verifiers run regardless of Focus. Session state is
committed only after successful output; `--all` leaves it unchanged. The
existing `workbench buildable verify` dispatch is untouched.

The committed action/Report digest fixture is generated using the TypeScript
reference's `Manifest.register` and `Engine.actionKey`:

```
bun internal/verify/engine/testdata/generate.mts /path/to/tools/verify/src
```

The engine's Docker tests exercise real snapshots, stores, selectors,
containers, and client presentation. They require the pinned Alpine and Bun
images and Pkl on PATH; no test is skipped. Run:

```
mise exec -- go test ./internal/verify/engine/... ./cmd/workbench/... -run Verify
mise exec -- go test -race ./internal/verify/engine/... -run Verify
mise exec -- go build -buildvcs=false -o bin/workbench ./cmd/workbench
```

The reference demo's nine steps and all behavior assertions pass. Its current
conformance golden differs only by the added `tool-failure` witness in the final
step, as required by the rewrite's store contract.
