# Verification store

This package is independent of the other verification packages. Callers supply
plain action facts and records; execution, Report validation, retained-path
selection, and audit sampling belong to the engine.

`ActionKey` hashes the TypeScript reference's ordered JSON array, including
JavaScript UTF-16 and tuple-string sorting. `IndexKey` adds the Tree only for
nondeterministic Verifiers. `PutDirectory` uses the reference directory manifest
format. Regenerate the checked-in goldens with:

```sh
bun internal/verify/store/testdata/generate.mts /path/to/monorepo/tools/verify/src
```

`Publish` exclusively creates an action index record. A collision returns the
stored record and writes a content-addressed witness when outputs differ. A
nonzero exit writes a `tool-failure` witness, returns `ErrFailureNotAttested`, and
never creates an index entry. Unlike the current reference's Engine, which
returns nonzero runs as unknown without evidence, this follows the rewrite
brief's failure-witness requirement. Timeouts or other refusals can be recorded
with `WriteWitness` without calling `Publish`.

For Scratch audits, the engine compares warm and cold outputs before publishing.
On mismatch it calls `WriteWitness` with kind `scratch-audit`, calls
`DropScratch` for that Verifier version, and returns unknown without publishing.
`RestoreScratch` requires the recorded version and refuses any version with a
witness. `SaveScratch` accepts only already-filtered retained entries.

`Collect` opens regular files with `O_NOFOLLOW` and `O_NONBLOCK`, stores symlink
text, and confines traversal with `os.Root`. `Materialize` confines writes with
`os.Root`, preserves symlinks and executable modes, and copies regular blobs so
writes to staged files cannot mutate CAS content. Store home and its CAS/index
are engine-owned directories, not sandbox writable mounts.
