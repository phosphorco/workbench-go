# Verification

`workbench verify` observes a Git repository's tracked working-tree bytes using
root `workbench.pkl`. It runs all declared Verifiers, reuses attested actions,
and presents the change since the current session last looked. Docker and its
pinned Verifier images must be available. The released executable bundles Pkl;
a development build uses Pkl on PATH.

```text
workbench verify [file:<glob>]... [code:<c>,<c>]... [unit:<id>]...
                 [member:<name>]... [verifier:<id>,<id>]...
                 [severity:<s>,<s>]... [focus:<name>]...
                 [--gate <name>] [--all] [--reset] [--json]
                 [--session ID] [--home DIR]
workbench verify promote <verifier> [--home DIR]
```

## Calls and state

```sh
workbench verify
workbench verify 'file:src/**' code:TYPE_ERROR,TEST_FAIL --gate commit
workbench verify focus:tests 'member:handles an empty input'
workbench verify --json --session review --home /tmp/verify-state
workbench verify --all
workbench verify --reset
```

| Option | Behavior |
| --- | --- |
| `--gate <name>` | Require the declared Gate over the entire Observation, including occurrences outside Focus. |
| `--json` | Emit `{observation, delta, focus}` instead of rendered text. |
| `--all` | Show every occurrence, including seen occurrences; leave Last Seen unchanged. |
| `--reset` | Clear this session's Last Seen before the normal call. Combined with `--all`, the reset still clears state. |
| `--session ID` | Override `PHOSPHOR_VERIFY_SESSION`, then `BB_THREAD_ID`, then `default`. |
| `--home DIR` | Override `VERIFY_HOME`, then `~/.verify`; relative paths resolve from the invocation directory. |
| `--help`, `-h` | Print usage without accessing the checkout. |

The state home contains CAS blobs, the action cache, retained Scratch, failure
witnesses, and `sessions/<session>/last-seen.json`. `VERIFY_CONCURRENCY` sets the
engine's worker limit; the default is four. Verification uses a temporary index
and private object storage: it does not write the checkout's Git index or HEAD.
Untracked files never enter the Tree. Declare and track all source, manifests,
and Verifier programs before verifying them.

A normal call writes Last Seen only after output succeeds. New occurrences are
grouped by error, file, and position; resolved occurrences include their last
anchor; unchanged occurrences are counted. Unknown Units retain their previous
occurrences in Last Seen because they were not observed. Resolved occurrences
leave Last Seen even when outside Focus; occurrences outside Focus that have
not been shown are not marked seen. If a resolved occurrence reappears, it is new.

## Focus grammar

Focus changes attention and the local diagnostic exit decision; it never
narrows engine work. Every Verifier still selects every Unit and runs cache
misses. Repeated values within a dimension are alternatives; populated
dimensions combine as an intersection. Empty dimensions are unrestricted.
Named Focuses merge their values with inline tokens.

| Token | Matches |
| --- | --- |
| `file:<glob>` | Repository-relative diagnostic paths; quote shell globs, e.g. `'file:src/**/*.ts'`. A fileless diagnostic cannot match. |
| `code:<c>,<c>` | Diagnostic codes or error keys. |
| `verifier:<id>,<id>` | Verifier IDs. |
| `unit:<id>` | Exact Unit IDs, with no implied path meaning. |
| `member:<name>` | Member names and diagnostic error keys; quote tokens containing spaces. |
| `severity:<s>,<s>` | `error`, `warning`, `suggestion`, or `message`. |
| `focus:<name>` | A named entry in `workbench.pkl`'s `focuses`. An unknown name is an error. |

`file`, `unit`, `member`, and `focus` each take one value per token; repeat them
for alternatives. File globs support `**` and a leading `!` negation. A Focus
call shows matching occurrences and members with an elsewhere summary. Only
`verifier` and `unit` dimensions can exclude an unknown Unit: its diagnostics
are unavailable, so file, code, member, and severity filters cannot exclude it.

## Exit codes

The current Observation determines exit status, even when the Delta has no new
occurrences. Blocking severities are the named Gate's `blocking` list, or
`error` when no Gate is named.

| Code | Meaning |
| --- | --- |
| `0` | No blocking occurrence in Focus, no unknown Unit in Focus, and any named Gate is satisfied. |
| `1` | A blocking occurrence in Focus, or the named Gate is unsatisfied. |
| `2` | Usage, Tree, declaration, or operational failure. |
| `3` | A Unit in Focus is unknown and no condition requiring `1` takes precedence. |

A Gate requires every Unit of each required Verifier to be attested and no
occurrence anywhere in the Observation to have a blocking severity. A failed
Verifier process is unknown, never an attested diagnostic-free success. Tool
failures are retried on the next call.

## Repository declaration

The `0.9.0` Pkl package adds `Verifier.pkl`, `Focus.pkl`, and the `verifiers`,
`gates`, and `focuses` properties to `Repository.pkl`. For offline verification,
use the embedded schemas via `modulepath:`:

```pkl
// workbench.pkl
amends "modulepath:/Repository.pkl"

verifiers {
  ["tests"] = import("verifiers/tests/VERIFIER.pkl")
}
gates {
  ["commit"] = new Gate {
    requires { "tests" }
    blocking { "error" }
  }
}
focuses {
  ["tests"] = new Focus { verifiers { "tests" } }
}
```

```pkl
// verifiers/tests/VERIFIER.pkl
amends "modulepath:/Verifier.pkl"

id = "tests"
image = "alpine@sha256:5291449c3df73caf6ed85e649dec1b9e818b39a5d8c871e97afc13e9cd5e8fa8"
program { "verifiers/tests/run.sh" }
command { "sh" "/verifier/run.sh" }
selector = new Paths {
  patterns {
    "src/**"
    "tests/**"
  }
}
produces = "report"
```

Use a pinned image containing your Verifier's toolchain; this example uses
the same Alpine digest as the engine tests. Track `run.sh`; it must write `/out/report.json`
with the supplied Unit ID, a `diagnostics` listing and `adviceById` mapping,
and exit zero when it completed, including when it found errors. A minimal
successful Report is `{"unitId":"tests","diagnostics":[],"adviceById":{}}` for the
single Unit of this Verifier. `Paths` and `WholeTree` use the Verifier ID as
the Unit ID; `Own` selection supplies its own Unit IDs.
See the [runnable examples](../examples/verify/README.md) for complete programs
and Reports.

`WholeTree` selects every tracked path as one Unit; `Paths` selects matching
tracked paths as one Unit; `Own` runs the Verifier command with
`VERIFY_ROLE=select`, reads Unit declarations from stdout, and caches that
selection. A Run gets precisely its selected inputs read-only at `/work`,
program files read-only at `/verifier`, declared upstream artifacts at
`/upstream/<name>`, writable `/out`, and optional retained `/scratch`. Inputs,
program bytes, command, manifest/image version, and upstream artifact digests
contribute to action identity. Unrelated Tree edits reuse deterministic actions;
nondeterministic Verifiers also bind their cache keys to the Tree.

`requires` maps mount names to registered artifact Verifier IDs. Artifacts
must declare nonempty `outputs`; Reports must leave `outputs` empty.
`toolchain` defaults to strong. a `Weak` with `retained { "cache/**" }` and `auditRate = 0.1`
permits declared Scratch paths across Runs; a failed clean audit makes the Unit
unknown and disables that version's Scratch. Network defaults to `none`;
`unrestricted` explicitly grants container networking. The default timeout is
five minutes.

Declaration evaluation reads frozen tracked Pkl documents from the Tree,
without ambient environment, untracked imports, or network access. External
packages require a tracked `PklProject` and `PklProject.deps.json` with pinned
checksums and previously acquired package metadata/ZIPs under
`<home>/pkl/cache/package-2`. Evaluation never downloads dependencies and ignores
project evaluator settings. Embedded `modulepath:` schemas need no acquisition.
Public consumers can also import the released package:
`package://github.com/phosphorco/workbench-go/releases/download/0.9.0/workbench@0.9.0#/Repository.pkl`.

## Promote an attested artifact

```sh
workbench verify promote tool-build --home /tmp/verify-state
```

Promotion captures the current Tree, evaluates its declaration, and observes
all Verifiers through the same engine, reusing cached actions and executing
misses. It leaves Last Seen unchanged. The named artifact Verifier must declare
`distribution` and have exactly one attested Unit:

```pkl
distribution = new Distribution {
  manifest = new ManifestContract {
    schemaVersion = 1
    kind = "tool"
    contractId = "tool-v1"
    requiredSourceFields { "revision" }
    requiredCapabilities { "run" }
  }
  platform = "linux/amd64"
  target = ".ci-build/tool"
}
```

Its artifact includes `manifest.json` in the existing Buildable wire format.
The contract, platform, complete output coverage, digests, sizes, and executable
modes must match the attested directory. Manifest outputs must be regular files.
Promotion verifies CAS and staged bytes, then replaces the target with the
complete artifact directory while confining writes to the repository. It prints
the artifact digest on success (`0`); refusals and errors return `2`. macOS
platforms refuse before execution because no macOS pool exists yet. See the
[distribution contract](../internal/verify/distribution/README.md) for manifest
fields and integrity rules.

Existing Buildables use `workbench buildable attest --name <name>
--candidate-root <path> [--run-declared-verification]` to recheck sealed manifest
facts and optionally run their declared verification command. In 0.9.0 this
replaces `buildable verify`; no compatibility shim ships in workbench-go.
