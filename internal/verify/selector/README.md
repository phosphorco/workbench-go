# Selector

`WholeTree(tracked, verifierID)` and `Paths(tracked, verifierID, patterns)` are
pure selections over tracked repository paths. Each yields one Unit with the
verifier ID and display, sorted unique inputs, and no references to caller slices.
`Paths` unions pattern matches. The committed Bun.Glob parity table covers both
patterns used by the demo and catalogue (`bun.lock`, `workspace.pkl`) and recursive,
brace, and character-class patterns. This is not a claim of parity for every
possible Bun glob syntax.

`Own(ctx, tracked, sandbox.Invocation)` uses a caller-staged whole Tree, program,
upstream mounts and fresh output directory. It forces role `select`, unit `*`
(stdin `{"unitId":"*"}`), and no Scratch. The sandbox mounts /work read-only.
Only a regular `/out/select.json` is accepted via `sandbox.ReadRegularOutput`;
artifact symlinks collected as mode `120000` remain refused for selection. The returned Outcome retains process
and output evidence for the engine. No cache is owned here.

`Decode(bytes, tracked)` also lets the engine validate cached select artifacts.
Malformed JSON, duplicate object keys and Unit IDs, non-string fields, escaping
paths, and untracked inputs are refused as errors without partial Units. Inputs
are deduplicated and sorted; Unit order is retained. An optional string `display`
is preserved, otherwise the Unit ID is used. Unknown extension fields are allowed.

`SelectAction(RunSpec, whole []Input)` exposes content-key ingredients: verifier
ID/version, select role, wildcard Unit ID, all whole-Tree path/digest/mode entries,
upstream name/digest entries, program path/digest/mode entries, command and image
digest. The engine supplies these content identities and owns serialization,
hashing, nondeterministic Tree salting, and cache publication. No Tree hash,
host staging path or Scratch enters this value.

## Reference differences

The TypeScript reference and current demo write selection JSON to stdout and the
engine stores that stdout as `select.json`. This package follows the commissioned
file protocol: the Verifier must write `/out/select.json`. The demo parity test
uses the unchanged demo program and pinned Bun image, with its command wrapped as
`sh -c 'bun /verifier/run.ts > /out/select.json'`. Engine integration must adapt
stdout-producing commands or update those Verifiers; there is no stdout fallback.
Duplicate Unit IDs, duplicate JSON keys, empty Unit IDs, invalid UTF-8 and escaping
paths are rejected more strictly than the reference decoder. `display` is an
additional requested result field.

Regenerate fixtures with:

```
bun internal/verify/selector/testdata/generate.mts /path/to/monorepo/tools/verify
```

The generator calls the reference Select module and demo sandbox harness with
Bun. Demo source and workspace-sync outputs are committed fixtures, making the Go
oracle independent of the monorepo checkout, Bun on the host, and Pkl on the host.
Docker and the pinned demo Bun and Alpine images are required; tests never skip.
