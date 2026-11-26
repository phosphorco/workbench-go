# Workbench 0.13.0

Workbench 0.13.0 lets a Repository describe an existing monorepo package layout
in typed Pkl, with a matching 0.13.0 Pkl contract. Every 0.12.0 and earlier
declaration keeps its released meaning: every 0.13.0 behavior below applies
only to 0.13.0 declarations, and the 0.12.0 contract is frozen byte for byte as
published.

- **Explicit package directories.** `PackagePolicy.directory` places a
  Repository package at a relative directory in the checkout, replacing leaf
  search.
  - The directory and its source roots must be real directories reached
    without symlinks.
  - No `..`, hidden, `node_modules` or glob segment is accepted.
  - Two packages cannot share a directory, and a package cannot sit inside
    another package's source root.
  - PackageScope packages still derive `<leaf>`.
- **Declared source roots and excludes.** `sourceRoots` (default: `src`) and
  `sourceExcludes` on both resource shapes drive the import scan, the dev/prod
  classification, and the generated tsconfig `include`, `exclude` and `rootDir`.
  - The `sourceRoots` default is an empty Listing, so amending it and assigning
    `new Listing { ... }` mean the same thing.
  - An exclude must exist and lie strictly inside a source root. It cannot also
    be a root.
- **In-repository PackageScopes.** `scopes { "@scope" }` on a Repository reads
  `pkg/<scope>/workbench.pkl`.
  - Each file amends the same-version PackageScopeRepository contract and is
    evaluated on its own. It cannot import modules or read files or the
    environment.
  - Its packages are placed at `pkg/<scope>/<leaf>`, and its includes merge into
    the Repository's.
  - Setup refuses a missing or symlinked scope file, another contract or
    version, a different scope, scope buildables, and a root package named
    inside a listed scope.
- **Verbatim dependency specifiers.** `dependencyVersions = "verbatim"` passes
  ranges, `npm:` aliases and relative path `file:`/`link:` specifiers through
  unchanged.
  - `catalog:` references are refused, because the generated root carries no
    catalog to resolve them.
  - Absolute paths, `link:<name>`, and paths escaping the Workbench root are
    also refused.
  - One dependency must still use one specifier across the closure.
  - Setup reports every root reassembly problem at once, each with its
    specifiers and declaring packages: catalog references, disagreements
    (including `typescript`), and non-exact versions in exact packages.
- **TypeScript-shaped observation.**
  - Hidden files and directories and `node_modules` below a source root are
    skipped.
  - Imports in ambient `.d.ts` files are observed from Workbench's lexer. Bun
    parses declaration files as implementation files and refuses valid ones.
  - `__tests__` is classified relative to the package.
  - Files beginning with `#!`, including after a UTF-8 byte order mark, are
    scanned. Bun's import scanner refuses `#!`, so Workbench reads the line as a
    `//` comment, keeping offsets and line numbers.
  - Bare runtime modules are not packages. The frozen set is Node 22 core
    modules plus `bun`. `ws` and `undici`, which Bun also lists, remain packages.

Known limits, left to later contracts:

- The generated root manifest has no catalog, overrides or patches, so verbatim
  closures must name root-resolvable specifiers.
- Peer dependency ranges still take part in root reassembly.
- Dev/prod classification for a source root not named `__tests__` follows file
  names only.

Install with `mise use github:phosphorco/workbench-go@0.13.0`. Release archives
include private pinned runtimes, licenses, checksums and attestations for
macOS/Linux on ARM64/x64, plus the `workbench@0.13.0` Pkl package.
