# Evaluation feasibility probe

Run from the repository root:

```text
python3 .context/workbench-context-declaration/probe/run.py
```

The command completed with exit status `0` on 2026-09-09. `run.py` exits
nonzero if the pinned runtime hash or any required owned-process, bounded
decode, memory, or file-freshness predicate fails. It does not fail on the
legacy `pkl-go` red witnesses.

## Reproducibility

- Go: `go1.26.6 linux/amd64`
- pkl-go: `github.com/apple/pkl-go v0.14.0`
- Pkl: `0.32.1`, `/home/ubuntu/.local/share/mise/installs/pkl/0.32.1/pkl`
- Pkl SHA-256: `3180b62da95c0cad1d904e9bb6c5f4a8f9032413c21e53194bb91ff1ee5f3211`
- `release/runtime-lock.json` SHA-256: `6f2a3df238ba7bc7e2210e7a26eb1ccac7556dbd9310b73f9159b95be59eb82c`
- `run.py` SHA-256: `698cd8a5e44e6187a933ed97d882b84a350c8a6097932b0a1a80e9ae6ee85036`
- `reader_probe.go` SHA-256: `c60d62c0c3a206d84ed2e7e98be54b8cb79a8ce9ee87ea105cad4cd79bd1e876`

Bounds are 3 s evaluator deadline, 4 MiB input, 1 MiB protocol/output bytes,
64 KiB diagnostics/stderr, protocol maps/arrays of at most 32 fields and
nesting depth 8, and 8 concurrent cold hooks. Home-only cold has 3 fresh
process samples; no-source has 200 discovery-only samples over depth 12.

## Measured results

- No-source discovery: p95 `0.041 ms`, max `0.223 ms`, no process and no
  writes. This is a Python discovery-only stand-in, not a full hook call.
- Home-only cold: wall p95 `33.916 ms`, evaluator p95 `8.221 ms`; all three
  evaluations made exactly one module-reader call.
- Warm evaluator: wall `24.567 ms`; first expression `0.334 ms`, second
  `0.089 ms`; the result carries the exact 45-byte entry capture with digest
  `28a485dbc009000b4e2e57ee76c75d9b51232a47386088a30e51c8d8e785e3bd` plus
  one exact 68-byte reader-import capture with digest
  `01d084449ac9055da3eb3b73bd921be646cec5ac99bd7d1b5efad411e05077d2`.
- Eight cold misses beside a 1 MiB minus 4096-byte filler: wall `88.837 ms`,
  p95 `86.861 ms`, fixture bytes unchanged at `1044480`. The helpers did not
  receive a cache/capacity path: this is explicitly not shared trace or
  snapshot capacity evidence.
- Eight fresh owned-server normal calls: wall `109.300 ms` against a 2 s
  total bound; all eight returned successfully with joined cleanup. This is
  evaluator lifecycle throughput only, not shared capacity.
- Owned server normal request/reply: wall `30.348 ms`, joined process group,
  exact reader capture, one-byte JSON result.
- Owned handshake cancellation: wall `145.305 ms` with a 100 ms context,
  process and reader/writer joined.
- Owned 2 MiB result rejected before result allocation (`2097157 > 1048576`);
  owned 2 MiB diagnostic rejected before allocation (`6291601 > 65536`).
- Owned blocked stdin write: a `4128768`-byte request to a non-reading,
  signal-ignoring fixture cancelled at 100 ms and joined in `123.739 ms`.
- The file helper accepted an unchanged capture after target+bytes
  revalidation, rejected the controlled retarget, and rejected the observed
  ABA capture even though its before/after endpoint targets were both A. The
  naive endpoint-only check still reports that ABA as missed.
- Controlled real-file symlink retarget changed A to B between resolution and
  read; the ABA case returned to A at both endpoints while the captured bytes
  were B. Both naive endpoint checks were marked unsafe, with regular-file and
  4 MiB checks applied.
- Child-only Linux `RLIMIT_DATA` controls at 128/256/512/1024 MiB all allowed
  `pkl eval -x '1 + 1' pkl:base`. At 128 MiB,
  `pkl eval -x '("x".repeat(200000000)).length' pkl:base` exited 99 in
  `156.014 ms` with an `OutOfMemoryError`. This is a Linux data-segment
  containment witness, not a portable Pkl heap or total-RSS bound; native
  overhead and emitted output still require production accounting.

## Minimal failing witnesses for the current evaluator route

- A 100 ms context around version discovery still spent `2007.109 ms` in
  `NewEvaluator` when a wrapper delayed `--version` for 2 s.
- A 100 ms blocked server handshake returned after `2038.819 ms`; manager
  close itself took `1903.532 ms`.
- The current pkl-go evaluator accepted a 2 MiB output and produced a
  `6291603`-byte diagnostic despite the 1 MiB/64 KiB probe budgets.

These are rejection evidence for the current pkl-go lifecycle/adapter, not a
claim that the owned subset is a production evaluator.

## Smallest proposed seams and obligations

1. Start the exact bundled binary through one owned worker/exec boundary. Do
   not perform an uncancellable version subprocess; identity comes from the
   pinned bundle/hash. A worker may apply a child-only platform limit before
   `syscall.Exec`; `MaxProcessMemoryBytes` must document platform semantics
   and native-RSS limitations.
2. Keep the owned MessagePack subset behind a typed bounded decoder. Bound
   envelope fields, container counts, nesting, strings, bytes, total protocol
   bytes, diagnostics, and input before allocating them. Close both pipes on
   cancellation, kill the owned process group, join every writer/reader and
   `Wait` before returning.
3. The reader seam resolves a designation once under the authority root,
   rejects non-regular/over-bound files, captures the exact supplied bytes and
   digest, and records the canonical target. The installed pkl-go callback
   exposes designation but no importing origin; the accepted contract is
   `ImportingOrigin.Known=false`, never a fabricated parser edge. Production
   should use a rooted-open/revalidation capability such as Go 1.26 `os.Root`.
4. The later loader/cache seam can use the coordinated
   `EvaluationInput`/`DependencyManifest` values and an opaque key containing
   origin, authority, schema/evaluator identity, and exact manifest. Snapshot
   reuse validates the stored manifest/revision before returning. Publish must
   take the shared reservation covering temp bytes and metadata. This probe
   has no cache writer and proves no trace/snapshot or shared-capacity
   integration.

The probe contains no production evaluator, reader, cache, or capacity
implementation.
