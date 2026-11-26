# Workbench 0.14.0

Workbench 0.14.0 lets a repository be its own Workbench. `workbench setup` then
computes every workspace `package.json`, every `tsconfig*.json`, the per-role
indexes, the root solution and the root `package.json` from Pkl declarations.
`workbench setup --check` compares them with the working tree without writing
anything. The 0.14.0 Pkl contract matches the binary.

Every 0.13.0 and earlier declaration keeps its released meaning. Every behavior
below is gated on a 0.14.0 *declaration*, and the 0.13.0 contract is frozen
byte for byte as published. A golden test projects 0.13 fixtures with the
current code and requires the exact bytes the released 0.13.0 code produced.

## In place

- **`InPlaceSubject`.** A `workbench-subject.pkl` amending
  `InPlaceSubject.pkl` has one `entrypoint` and no work line. It is its own
  Subject shape, not a flag.
  - `git remote get-url origin` (HTTPS, SSH or scp form) must name the
    entrypoint, and the Workbench root must be the top of the checkout. Git is
    only read.
  - Declarations are read from the working tree: `workbench.pkl` and its
    in-repository scopes.
  - Refused:
    - cross-repository `includes`;
    - buildables;
    - a declaration other than 0.14.0;
    - a package without a `directory` or scope placement;
    - a Repository without an authored `root`.
- **The write set is the owned files only.** In-place setup:
  - reconciles no checkouts and projects no buildables;
  - writes no `managed-checkouts.json`, `AGENTS.md` or skills, and reports no
    orphans;
  - runs `bun install --frozen-lockfile` only when the Subject sets
    `install = true`.
- **Owned files**, which define "missing" and "extra" for `--check`:
  - `package.json`, `tsconfig.json` and `tsconfig.*.json` in placed package
    directories;
  - base files and standalone project files;
  - `.tsconfigs/**` under index directories and package directories;
  - the root `package.json` and `tsconfig.json`.

  Only tracked files can be owned. Untracked and ignored files are never
  owned. Setup writes the owned files that differ and deletes extra ones.
- **`workbench setup --check [--json]`.**
  - It computes the full projection, writes nothing and runs no reconciler.
  - It exits 0 when every owned file is byte-equal to the projection.
    Otherwise it exits 1 and lists each differing file with a unified diff,
    plus missing and extra owned files.
  - Human output shows at most 20 diffs and says how many it omitted.
    `--json` is never capped.
  - Informational findings that do not fail the check appear in both forms:
    - stale `include`, `exclude` or `files` entries;
    - reference fallbacks;
    - specifier disagreements;
    - the sources-graph summary (units, cycle sizes, circular marks).
- **`workbench commit` in place** admits an owned file only when it equals the
  `--check` projection. The commit branch is the checked-out branch.

## Observation

- **Input.** Observation reads the working tree minus gitignored files. A new
  source file creates its edges before `git add`.
- **File selection.** Each role compiles exactly the files TypeScript selects:
  - implicit directory globs;
  - `*` and `**` never matching dot entries or package folders;
  - prefix excludes;
  - include and exclude inherited from bases, with `${configDir}`;
  - `.ts`, `.tsx`, `.mts`, `.cts` and declaration files.

  The acceptance gate compares every role, standalone project and base with
  TypeScript's own `parseJsonConfigFileContent`.
- **Type-only imports.** `import type`, `export type` and all-`type` brace
  lists are observed. Bun erases them, but the compiler needs the dependency's
  declarations.

## Roles, bases and references

- **Roles.**
  - `PackagePolicy.roles` declares one `tsconfig.<role>.json` per role.
    `include`, `exclude` and `files` are written verbatim: null omits a key,
    and `[]` renders `[]`.
  - An empty `roles` builds one `sources` role from `sourceRoots` and
    `sourceExcludes`.
  - Overlaps must be declared with `alsoIncludes`. The role that does not
    declare it owns the shared file.
  - A file outside the package must be a `files` entry declared in
    `foreignFiles`. Its imports belong to the declaring role.
  - `development` roles add `devDependencies`.
- **Bases.**
  - `typescript.bases` writes `tsconfig.base.json` files.
  - A role extends the nearest enclosing base unless it names one, or sets
    `extendsBase = false`.
- **Workbench owns `composite`, `outDir`, `rootDir` and `tsBuildInfoFile`.**
  - Workbench writes `composite: true` and `outDir: "${configDir}/dist-types"`
    into every root base (a base extending no other).
  - It writes `${configDir}/dist-types/<role>` into every emitting non-sources
    role.
  - A role without a base gets both keys only when it emits.
  - Authors may not set these keys in roles or bases.
  - `rootDir` is the exception: it is an authored base option, refused in
    roles. With `rootDir: "./"` in a root base, packages emit
    `dist-types/<package path>/src/...`, and the Dagger declaration handoff
    consumes that layout. Workbench neither writes nor moves it.
  - `tsBuildInfoFile` is never written, so TypeScript's default stays.
- **References.**
  - A role whose `deriveReferences` is true (by default only `sources`)
    references, for each imported workspace package, the dependency role that
    compiles the imported file.
  - When that file is in no role, or in a role that does not emit, the
    reference falls back to the importing role's `referenceRole` and is
    reported as informational.
  - Other roles use `consumes` plus authored `references`. An entry
    `"<package>"` names the package's `referenceRole`. `"<package>#<role>"`
    names a role, which must exist and emit.
  - A test role can later move to derived references by changing its
    declaration.
  - A cross-package reference to a role that does not emit is refused.
- **Circular edges are derived by repository tooling, never authored.**
  - `circularEdges` names a generated file, for example
    `.workbench/circular-edges.json`, holding package edges
    `"source -> target"`.
  - Marks apply to sources→sources references.
  - Workbench refuses the set unless every mark is a derived edge inside a
    cycle, the unmarked graph is acyclic, and no package cycles through its own
    roles. The remedy names the generating file.
- **Indexes are derived.**
  - The repository root, and every base directory with packages beneath it,
    gets `.tsconfigs/tsconfig.<role>.child-index.json` for every role found
    beneath it.
  - It also gets a `tsconfig.json` that references those indexes and the
    standalone projects in that directory. At the root, this file is the
    solution.
- **Standalone projects.** `typescript.projects` declares repository projects
  that belong to no package, keyed by tsconfig path. Projects outside every
  package contribute their workspace imports to the root's dependencies.

## Manifests

- **Package fields.**
  - `fields` passes `version`, `sideEffects`, `bin`, `scripts`, `files`,
    `license` and similar fields through in declared order. Owned keys are
    refused.
  - `private: Boolean? = true`, where null omits the field.
  - No `exports` are invented: without declared exports the field is omitted.
  - One dependency may be in `peerDependencies` and `devDependencies`. Every
    other pair is still refused.
- **The authored root is a placed package.**
  - `root.packageJson.fields` carries the root fields in authored order.
  - `workspace:*` entries in root dependencies must name placed packages. The
    rendered dependencies are those authored entries plus the ones derived
    from root projects.
  - Authored `workspaces` globs must expand to exactly the placed package
    directories.
- **Catalog admission.**
  - `catalog:` must name a key of `catalog` (or `catalogs.default`), and
    `catalog:<name>` a key of `catalogs.<name>`.
  - `patchedDependencies` paths must exist. `overrides` pass through.
  - Specifier disagreements across packages are informational.
- **Rendering.**
  - JSON is written with 2-space indentation, LF line endings, a trailing
    newline and no HTML escaping.
  - tsconfig keys are in this order: `//, $schema, extends, typeAcquisition,
    compilerOptions, files, include, exclude, references`.
  - package.json keys are in this order: `//, name, version, private, type,
    <fields>, imports, exports, dependencies, devDependencies,
    peerDependencies, optionalDependencies`.
  - Authored `compilerOptions` and fields keep their declared order.
  - References are sorted by path, with `circular` after `path`.
  - `generatedMarker`, when set, is the first key `"//"` of every owned file.

## What the authored root gives up

Under an authored root, Workbench no longer reassembles the root manifest:

- It no longer checks for one exact `typescript` across the closure. That is
  replaced by a check that the root declares `typescript`.
- It no longer owns root `typecheck`/`test` scripts. The repository authors
  them.

## Known limits

- A context Workbench (a `WorkbenchSubject` with checkouts) projects a 0.14.0
  declaration with the 0.13 workspace projection. It refuses `roles`,
  `typescript`, `root`, `circularEdges`, `generatedMarker`, package `fields`
  and `private = false`, because it would otherwise ignore them.
- Workspace globs are matched with one-segment wildcards, as Bun matches the
  repository's globs; `**` is not expanded.

Install with `mise use github:phosphorco/workbench-go@0.14.0`. Release archives
include private pinned runtimes, licenses, checksums and attestations for
macOS/Linux on ARM64/x64, plus the `workbench@0.14.0` Pkl package.
