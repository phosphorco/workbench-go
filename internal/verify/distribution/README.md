# Attested artifact promotion

`workbench verify promote <verifier> [--home DIR]` computes the current tracked
Tree, evaluates its declaration, and obtains its Observation through the engine.
Cached actions are reused; misses run only through the engine. Promotion does not
update Last Seen. An unknown or failing artifact Unit refuses with its reason.
An artifact Verifier must have exactly one attested Unit to be promoted.

An artifact Verifier can declare:

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

The Pkl `ManifestContract` is an alias of `Repository.pkl`'s existing class;
the Go distribution field uses `internal/buildable.ManifestContract` directly.
`target` reuses `Repository.RelativePath`, matching Buildable's committed candidate
root. The whole attested artifact directory replaces that target. This is the
existing Buildable promotion layout, including `manifest.json`; output
`destination` facts remain in the manifest rather than remapping candidate paths.

The artifact's `manifest.json` uses the existing Buildable wire shape:
`schemaVersion`, `kind`, `contractId`, optional `declarationIdentity`, `source`,
`producerInputs { algorithm, digest }`, `capabilities`, and
`outputs [{ platform, path, destination?, kind?, sha256, size, executable }]`.
Identity, expected and required source fields, and required capabilities must
satisfy the contract. Output entries must cover the artifact's files apart from
`manifest.json`, match the named platform, and agree with attested digests, sizes,
and executable modes. Manifest outputs must be regular files; symlink outputs
are refused. Producer freshness comes from the current Tree's engine attestation,
so no Buildable input detection, build, seal, or verification command is run.

The store's directory and file digests are checked before materialization, and
the staged directory's digest must equal the attested artifact digest before
replacement. Promotion copies bytes, preserving executable modes without sharing
mutable files with the store. Repository confinement uses `os.Root`, including
when a target's ancestor is a symlink. Promotion reports the artifact digest.

Darwin platforms refuse before engine execution with
`no macOS pool exists yet; keep this as a Buildable`.

An unset `distribution` is null in Pkl but absent in rendered JSON:
`JsonRenderer.omitNullProperties` defaults to true. The Go field also uses
`omitempty`. The existing Pkl parity tests compare rendered Verifier bytes with
the unchanged reference; the nine-step conformance golden verifies that versions,
action keys, Observations, and client output remain unchanged.
