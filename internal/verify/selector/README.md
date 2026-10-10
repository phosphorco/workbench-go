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
Selection JSON is decoded from stdout. The returned Outcome retains the exact
stdout bytes and process evidence for the engine, which stores those bytes as
the cached select Action output named `select.json`. Files under /out never
supply the selection. No cache is owned here.

`Decode(bytes, tracked)` also lets the engine validate cached select artifacts.
Malformed JSON, duplicate object keys and Unit IDs, non-string fields, escaping
paths, and untracked inputs are refused as errors without partial Units. Inputs
are deduplicated and sorted; Unit order is retained. Display is always the Unit ID,
matching `Engine.mts` (`const display = unit.unitId`).
An optional `display` extension is still checked for string type but cannot override
the engine-derived display. Unknown extension fields are allowed.

`SelectAction(RunSpec, whole []Input)` exposes content-key ingredients: verifier
ID/version, select role, wildcard Unit ID, all whole-Tree path/digest/mode entries,
upstream name/digest entries, program path/digest/mode entries, command and image
digest. The engine supplies these content identities and owns serialization,
hashing, nondeterministic Tree salting, and cache publication. No Tree hash,
host staging path or Scratch enters this value.

## Reference differences

The stdout protocol and exact demo command `bun /verifier/run.ts` match the
reference. Duplicate Unit IDs, duplicate JSON keys, empty Unit IDs, invalid UTF-8,
escaping paths and non-string display extensions are rejected more strictly than
the reference decoder. The result's additional Display field equals the Unit ID.

Regenerate fixtures with:

```
bun internal/verify/selector/testdata/generate.mts /path/to/monorepo/tools/verify
```

The generator calls the reference Select module and demo sandbox harness with
Bun. Demo source and workspace-sync outputs are committed fixtures, making the Go
oracle independent of the monorepo checkout, Bun on the host, and Pkl on the host.
Docker and the pinned demo Bun and Alpine images are required; tests never skip.
