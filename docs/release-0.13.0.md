# Workbench 0.13.0

Workbench 0.13.0 lets a Repository describe an existing monorepo package layout
in typed Pkl, with a matching 0.13.0 Pkl contract. Every 0.12.0 and earlier
declaration is still admitted with its released meaning; the 0.12.0 contract is
frozen byte for byte as published.

- **Explicit package directories.** `PackagePolicy.directory` places a
  Repository package at a relative directory in the checkout, replacing leaf
  search. The directory and its source roots must be real directories reached
  without symlinks; no `..`, hidden or `node_modules` segment is accepted, and
  two packages cannot share a directory. PackageScope packages still derive
  `<leaf>`.
- **Declared source roots and excludes.** `sourceRoots` (default: `src`) and
  `sourceExcludes` on both resource shapes drive the import scan, the dev/prod
  classification and the generated tsconfig `include`, `exclude` and `rootDir`.
  The `sourceRoots` default is an empty Listing, so amending it and assigning
  `new Listing { ... }` mean the same thing.
- **In-repository PackageScopes.** `scopes { "@scope" }` on a Repository reads
  `pkg/<scope>/workbench.pkl`. Each file amends the same-version
  PackageScopeRepository contract and is evaluated on its own. Its packages are
  placed at `pkg/<scope>/<leaf>` and its includes merge into the Repository.
  Setup refuses a missing or symlinked scope file, another contract or version,
  a different scope, scope buildables, and a package declared twice.
- **Verbatim dependency specifiers.** `dependencyVersions = "verbatim"` passes
  `catalog:`, `catalog:<name>`, ranges, `npm:` aliases and `file:` specifiers
  through unchanged. One dependency must still use one specifier across the
  closure. Setup lists every conflicting dependency at once with its specifiers
  and declaring packages; it does not pick one.
- **TypeScript-shaped observation (0.13.0 declarations).** Hidden files and
  directories and `node_modules` below a source root are skipped. Ambient
  `.d.ts` files are not scanned: they carry no runtime imports, and Bun parses
  them as implementation files and refuses valid declarations. `__tests__` is
  classified relative to the package. Bare runtime built-ins reported by the
  pinned Bun (`node:module` `builtinModules`, plus `bun`) are not packages.
- **Shebang files.** Every contract version now scans files beginning with `#!`.
  Bun's import scanner refused them; Workbench turns the first two bytes into
  `//`, so offsets and line numbers are unchanged.

The generated root manifest still has no package catalog. A closure that uses
verbatim `catalog:` specifiers therefore reaches `bun install` and fails there
until the root can carry authored catalogs.

Install with `mise use github:phosphorco/workbench-go@0.13.0`. Release archives
include private pinned runtimes, licenses, checksums and attestations for
macOS/Linux on ARM64/x64, plus the `workbench@0.13.0` Pkl package.
