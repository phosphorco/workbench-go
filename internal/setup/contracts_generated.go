package setup

// Generated from ../../pkl/WorkbenchSubject.pkl. Do not edit independently.
const localSubjectContract = `module phosphor.workbench.WorkbenchSubject

typealias NonEmptyString = String(length > 0)
typealias BranchName = NonEmptyString
typealias GitHubRepositoryUrl =
  String(matches(Regex(#"https://github\.com/[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+(?:\.git)?"#)))

class WorkLine {
  branch: BranchName
  baseBranch: BranchName
}

workLine: WorkLine
entrypoints: Listing<GitHubRepositoryUrl>(length > 0)
`

// Kept byte-for-byte as the local 0.1 development schema. The unversioned
// workbench-contract URI is the already-released compatibility seam.
const legacyLocalRepositoryContract = `module phosphor.workbench.PackageScopeRepository

typealias NonEmptyString = String(length > 0)
typealias PackageScope = String(matches(Regex(#"@[a-z0-9][a-z0-9._-]*"#)))
typealias PackageName =
  String(
    length > 0,
    length <= 214,
    matches(Regex(#"(?:@[a-z0-9][a-z0-9._-]*/)?[a-z0-9][a-z0-9._-]*"#)),
  )
typealias GitHubRepository =
  String(matches(Regex(#"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+"#)), !endsWith(".git"))
typealias SkillDomain = "orchestration" | "engineering" | "general"
typealias SkillName = String(matches(Regex(#"[a-z0-9][a-z0-9-]*"#)))
typealias SkillSelection = "all" | SkillRoots
class SkillRoots {
  domains: Set<SkillDomain> = Set()
  names: Set<SkillName> = Set()
}

class SkillPolicy {
  editing: SkillSelection = new SkillRoots {}
  workbench: SkillSelection = new SkillRoots {}
}

class Include {
  github: GitHubRepository
  skills: SkillPolicy = new SkillPolicy {}
}

class PackagePolicy {
  requiredButNotReferenced: Mapping<PackageName, NonEmptyString> = new {}
  peerDependencies: Mapping<PackageName, NonEmptyString> = new {}
  optionalDependencies: Mapping<PackageName, NonEmptyString> = new {}
}

scope: PackageScope
includes: Mapping<PackageScope, Include> = new {}
packages: Mapping<PackageName, PackagePolicy> = new {}
`

// Kept byte-for-byte as the local 0.2 development schema. The 0.2 contract
// allowed package placement to be inferred from observed source layout.
const localV020RepositoryContract = `module phosphor.workbench.PackageScopeRepository

typealias NonEmptyString = String(length > 0)
typealias PackageScope = String(matches(Regex(#"@[a-z0-9][a-z0-9._-]*"#)))
typealias PackageName =
  String(
    length > 0,
    length <= 214,
    matches(Regex(#"(?:@[a-z0-9][a-z0-9._-]*/)?[a-z0-9][a-z0-9._-]*"#)),
  )
typealias GitHubRepository =
  String(matches(Regex(#"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+"#)), !endsWith(".git"))
typealias SkillDomain = "orchestration" | "engineering" | "general"
typealias SkillName = String(matches(Regex(#"[a-z0-9][a-z0-9-]*"#)))
typealias SkillSelection = "all" | SkillRoots

class SkillRoots {
  domains: Set<SkillDomain> = Set()
  names: Set<SkillName> = Set()
}

class SkillPolicy {
  editing: SkillSelection = new SkillRoots {}
  workbench: SkillSelection = new SkillRoots {}
}

class Include {
  skills: SkillPolicy = new SkillPolicy {}
}

class PackagePolicy {
  requiredButNotReferenced: Mapping<PackageName, NonEmptyString> = new {}
  peerDependencies: Mapping<PackageName, NonEmptyString> = new {}
  optionalDependencies: Mapping<PackageName, NonEmptyString> = new {}
}

scope: PackageScope
includes: Mapping<GitHubRepository, Include> = new {}
packages: Mapping<PackageName, PackagePolicy> = new {}
`

// Frozen from the immutable 0.3.0-0.5.0 PackageScopeRepository contract.
// New declarations must use localRepositoryContract instead.
const localV050RepositoryContract = `module phosphor.workbench.PackageScopeRepository

typealias NonEmptyString = String(length > 0)
typealias PackageScope = String(matches(Regex(#"@[a-z0-9][a-z0-9._-]*"#)))
typealias PackageName =
  String(
    length > 0,
    length <= 214,
    matches(Regex(#"(?:@[a-z0-9][a-z0-9._-]*/)?[a-z0-9][a-z0-9._-]*"#)),
  )
typealias GitHubRepository =
  String(matches(Regex(#"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+"#)), !endsWith(".git"))
typealias SkillDomain = "orchestration" | "engineering" | "general"
typealias SkillName = String(matches(Regex(#"[a-z0-9][a-z0-9-]*"#)))
typealias SkillSelection = "all" | SkillRoots

class SkillRoots {
  domains: Set<SkillDomain> = Set()
  names: Set<SkillName> = Set()
}

class SkillPolicy {
  editing: SkillSelection = new SkillRoots {}
  workbench: SkillSelection = new SkillRoots {}
}

class Include {
  skills: SkillPolicy = new SkillPolicy {}
}

class PackagePolicy {
  requiredButNotReferenced: Mapping<PackageName, NonEmptyString> = new {}
  peerDependencies: Mapping<PackageName, NonEmptyString> = new {}
  optionalDependencies: Mapping<PackageName, NonEmptyString> = new {}
}

scope: PackageScope
includes: Mapping<GitHubRepository, Include> = new {}
/// A PackageScope is always a container. Every package identity derives one
/// child directory from its unscoped leaf, regardless of package cardinality.
packages: Mapping<PackageName, PackagePolicy>(keys.every((name) -> name.startsWith(scope + "/"))) = new {}
`

// Frozen from the immutable 0.2.0-0.5.0 Repository contract.
const localV050RepositoryDeclarationContract = `module phosphor.workbench.Repository

typealias NonEmptyString = String(length > 0)
typealias PackageName =
  String(
    length > 0,
    length <= 214,
    matches(Regex(#"(?:@[a-z0-9][a-z0-9._-]*/)?[a-z0-9][a-z0-9._-]*"#)),
  )
typealias GitHubRepository =
  String(matches(Regex(#"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+"#)), !endsWith(".git"))
typealias SkillDomain = "orchestration" | "engineering" | "general"
typealias SkillName = String(matches(Regex(#"[a-z0-9][a-z0-9-]*"#)))
typealias SkillSelection = "all" | SkillRoots

class SkillRoots {
  domains: Set<SkillDomain> = Set()
  names: Set<SkillName> = Set()
}

class SkillPolicy {
  editing: SkillSelection = new SkillRoots {}
  workbench: SkillSelection = new SkillRoots {}
}

class Include {
  skills: SkillPolicy = new SkillPolicy {}
}

class PackagePolicy {
  requiredButNotReferenced: Mapping<PackageName, NonEmptyString> = new {}
  peerDependencies: Mapping<PackageName, NonEmptyString> = new {}
  optionalDependencies: Mapping<PackageName, NonEmptyString> = new {}
}

/// Repository identity is derived from the normalized GitHub designation used
/// to acquire this declaration; it is intentionally not authored here.
includes: Mapping<GitHubRepository, Include> = new {}
packages: Mapping<PackageName, PackagePolicy> = new {}
`

// Generated from ../../pkl/PackageScopeRepository.pkl. Do not edit independently.
const localRepositoryContract = `module phosphor.workbench.PackageScopeRepository

typealias NonEmptyString = String(length > 0)
typealias PackageScope = String(matches(Regex(#"@[a-z0-9][a-z0-9._-]*"#)))
typealias PackageName =
  String(
    length > 0,
    length <= 214,
    matches(Regex(#"(?:@[a-z0-9][a-z0-9._-]*/)?[a-z0-9][a-z0-9._-]*"#)),
  )
typealias PackageImport = String(length > 1, startsWith("#"), !contains(".."))
typealias PackageExport = "." | String(length > 2, startsWith("./"), !contains(".."))
typealias PackageTarget =
  String(length > 2, startsWith("./"), !contains("\\"), !contains("../"))
typealias GitHubRepository =
  String(matches(Regex(#"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+"#)), !endsWith(".git"))
typealias SkillDomain = "orchestration" | "engineering" | "general"
typealias SkillName = String(matches(Regex(#"[a-z0-9][a-z0-9-]*"#)))
typealias SkillSelection = "all" | SkillRoots
typealias BuildableName = String(matches(Regex(#"[a-z0-9][a-z0-9._-]*"#)))
typealias RelativePath =
  String(
    length > 0,
    !startsWith("/"),
    !contains("\\"),
    !matches(Regex(#".*[\x00-\x1f\x7f].*"#)),
    this != ".",
    this != "..",
    !startsWith("../"),
    !contains("/../"),
    !endsWith("/.."),
  )

class SkillRoots {
  domains: Set<SkillDomain> = Set()
  names: Set<SkillName> = Set()
}

class SkillPolicy {
  editing: SkillSelection = new SkillRoots {}
}

class Include {
  skills: SkillPolicy = new SkillPolicy {}
}

class PackagePolicy {
  dependencies: Mapping<PackageName, NonEmptyString> = new {}
  devDependencies: Mapping<PackageName, NonEmptyString> = new {}
  requiredButNotReferenced: Mapping<PackageName, NonEmptyString> = new {}
  peerDependencies: Mapping<PackageName, NonEmptyString> = new {}
  optionalDependencies: Mapping<PackageName, NonEmptyString> = new {}
  imports: Mapping<PackageImport, PackageTarget> = new {}
  exports: Mapping<PackageExport, PackageTarget> = new {}
}

class GitHeadTreeInputDetection {
  strategy: "gitHeadTree" = "gitHeadTree"
  paths: Listing<RelativePath>(length > 0)
}

class BuildCommand {
  executable: NonEmptyString
  arguments: Listing<NonEmptyString> = new {}
}

class ManifestContract {
  schemaVersion: UInt16(isPositive)
  kind: NonEmptyString
  contractId: NonEmptyString
  expectedSource: Mapping<NonEmptyString, NonEmptyString> = new {}
  requiredSourceFields: Listing<NonEmptyString> = new {}
  requiredCapabilities: Listing<NonEmptyString> = new {}
}

class BuildableCandidate {
  /// Opaque candidate identity returned by Workbench admission. Older
  /// declarations may omit it; Workbench applies a compatibility identity.
  identity: NonEmptyString? = null
  root: RelativePath
  inputStrategy: "gitWorktree" | "gitHeadTree"
  invalidRemedy: NonEmptyString
}

class BuildableOutput {
  path: RelativePath
  destination: RelativePath
  kind: NonEmptyString
  executable: Boolean = false
}

class BuildablePlatformOutput {
  os: Listing<NonEmptyString>(length > 0)
  arch: Listing<NonEmptyString>(length > 0)
  /// Legacy single-output field retained so 0.6 declarations remain parseable.
  path: RelativePath? = null
  executable: Boolean = true
  outputs: Listing<BuildableOutput> = new {}
}

class Buildable {
  inputDetection: GitHeadTreeInputDetection
  buildCommand: BuildCommand
  /// Optional repository-owned composition proof invoked only by the explicit
  /// buildable verify command.
  verificationCommand: BuildCommand? = null
  manifest: ManifestContract
  /// Preference is authored order. A present-invalid candidate refuses; it
  /// never falls through to a later candidate.
  candidates: Listing<BuildableCandidate>(length > 0)
  /// Keys are the platform names recorded by the artifact manifest.
  platforms: Mapping<NonEmptyString, BuildablePlatformOutput>(length > 0)
}

scope: PackageScope
includes: Mapping<GitHubRepository, Include> = new {}
/// A PackageScope is always a container. Every package identity derives one
/// child directory from its unscoped leaf, regardless of package cardinality.
packages: Mapping<PackageName, PackagePolicy>(keys.every((name) -> name.startsWith(scope + "/"))) = new {}
buildables: Mapping<BuildableName, Buildable> = new {}
`

// Generated from ../../pkl/Repository.pkl.
const localRepositoryDeclarationContract = `module phosphor.workbench.Repository

import "Verifier.pkl"
import "Focus.pkl" as FocusModule

typealias Focus = FocusModule.Focus

typealias NonEmptyString = String(length > 0)
typealias PackageName =
  String(
    length > 0,
    length <= 214,
    matches(Regex(#"(?:@[a-z0-9][a-z0-9._-]*/)?[a-z0-9][a-z0-9._-]*"#)),
  )
typealias PackageImport = String(length > 1, startsWith("#"), !contains(".."))
typealias PackageExport = "." | String(length > 2, startsWith("./"), !contains(".."))
typealias PackageTarget =
  String(length > 2, startsWith("./"), !contains("\\"), !contains("../"))
typealias GitHubRepository =
  String(matches(Regex(#"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+"#)), !endsWith(".git"))
typealias SkillDomain = "orchestration" | "engineering" | "general"
typealias SkillName = String(matches(Regex(#"[a-z0-9][a-z0-9-]*"#)))
typealias SkillSelection = "all" | SkillRoots
typealias BuildableName = String(matches(Regex(#"[a-z0-9][a-z0-9._-]*"#)))
typealias RelativePath =
  String(
    length > 0,
    !startsWith("/"),
    !contains("\\"),
    !matches(Regex(#".*[\x00-\x1f\x7f].*"#)),
    this != ".",
    this != "..",
    !startsWith("../"),
    !contains("/../"),
    !endsWith("/.."),
  )

class SkillRoots {
  domains: Set<SkillDomain> = Set()
  names: Set<SkillName> = Set()
}

class SkillPolicy {
  editing: SkillSelection = new SkillRoots {}
}

class Include {
  skills: SkillPolicy = new SkillPolicy {}
}

class PackagePolicy {
  dependencies: Mapping<PackageName, NonEmptyString> = new {}
  devDependencies: Mapping<PackageName, NonEmptyString> = new {}
  requiredButNotReferenced: Mapping<PackageName, NonEmptyString> = new {}
  peerDependencies: Mapping<PackageName, NonEmptyString> = new {}
  optionalDependencies: Mapping<PackageName, NonEmptyString> = new {}
  imports: Mapping<PackageImport, PackageTarget> = new {}
  exports: Mapping<PackageExport, PackageTarget> = new {}
}

class GitHeadTreeInputDetection {
  strategy: "gitHeadTree" = "gitHeadTree"
  paths: Listing<RelativePath>(length > 0)
}

class BuildCommand {
  executable: NonEmptyString
  arguments: Listing<NonEmptyString> = new {}
}

class ManifestContract {
  schemaVersion: UInt16(isPositive)
  kind: NonEmptyString
  contractId: NonEmptyString
  expectedSource: Mapping<NonEmptyString, NonEmptyString> = new {}
  requiredSourceFields: Listing<NonEmptyString> = new {}
  requiredCapabilities: Listing<NonEmptyString> = new {}
}

class BuildableCandidate {
  /// Opaque candidate identity returned by Workbench admission. Older
  /// declarations may omit it; Workbench applies a compatibility identity.
  identity: NonEmptyString? = null
  root: RelativePath
  inputStrategy: "gitWorktree" | "gitHeadTree"
  invalidRemedy: NonEmptyString
}

class BuildableOutput {
  path: RelativePath
  destination: RelativePath
  kind: NonEmptyString
  executable: Boolean = false
}

class BuildablePlatformOutput {
  os: Listing<NonEmptyString>(length > 0)
  arch: Listing<NonEmptyString>(length > 0)
  /// Legacy single-output field retained so 0.6 declarations remain parseable.
  path: RelativePath? = null
  executable: Boolean = true
  outputs: Listing<BuildableOutput> = new {}
}

class Buildable {
  inputDetection: GitHeadTreeInputDetection
  buildCommand: BuildCommand
  /// Optional repository-owned composition proof invoked only by the explicit
  /// buildable verify command.
  verificationCommand: BuildCommand? = null
  manifest: ManifestContract
  /// Preference is authored order. A present-invalid candidate refuses; it
  /// never falls through to a later candidate.
  candidates: Listing<BuildableCandidate>(length > 0)
  /// Keys are the platform names recorded by the artifact manifest.
  platforms: Mapping<NonEmptyString, BuildablePlatformOutput>(length > 0)
}

/// Repository identity is derived from the normalized GitHub designation used
/// to acquire this declaration; it is intentionally not authored here.
includes: Mapping<GitHubRepository, Include> = new {}
packages: Mapping<PackageName, PackagePolicy> = new {}
buildables: Mapping<BuildableName, Buildable> = new {}

/// A policy naming which Verifiers must be attested for a Tree at a boundary.
class Gate {
  requires: Listing<String(!isEmpty)>(!isEmpty, isDistinct)
  /// Severities that block; empty means only attestation is required.
  blocking: Listing<FocusModule.Severity>(isDistinct)
}

/// Keyed by Verifier id; every ` + "`" + `requires` + "`" + ` names a registered artifact Verifier.
verifiers: Mapping<String, Verifier>(
  toMap().every((key, verifier) -> verifier.id == key),
  toMap().values.every((verifier) ->
    verifier.requires.toMap().values.every((upstream) ->
      containsKey(upstream) && this[upstream].produces == "artifact"))
) = new {}

/// Every Gate requires registered Verifiers.
gates: Mapping<String, Gate>(
  toMap().values.every((gate) -> gate.requires.toList().every((id) -> verifiers.containsKey(id)))
) = new {}

focuses: Mapping<String, Focus> = new {}

output {
  renderer = new JsonRenderer {
    converters {
      [Duration] = (duration) -> duration.toUnit("s").value
    }
  }
}
`

// Generated from ../../pkl/AgentInstructions.pkl.
const localAgentInstructionsContract = `module phosphor.workbench.AgentInstructions

typealias NonEmptyString = String(length > 0)
typealias BranchName = NonEmptyString
typealias GitHubRepository =
  String(matches(Regex(#"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+"#)), !endsWith(".git"))
typealias GitHubRepositoryUrl =
  String(matches(Regex(#"https://github\.com/[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+(?:\.git)?"#)))
typealias PackageScope = String(matches(Regex(#"@[a-z0-9][a-z0-9._-]*"#)))
typealias ResourceIdentity = PackageScope | GitHubRepository
typealias CanonicalPath = String(length > 0, !startsWith("/"))
typealias ResourceHealth = "healthy" | "missingCheckout" | "wrongBranch"

class WorkLine {
  branch: BranchName
  baseBranch: BranchName
}

class PackageScopeShape {
  kind: "packageScope" = "packageScope"
  scope: PackageScope
}

class RepositoryShape {
  kind: "repository" = "repository"
}

typealias ResourceShape = PackageScopeShape | RepositoryShape

class SubjectFacts {
  workLine: WorkLine
  entrypoints: Listing<GitHubRepositoryUrl>(length > 0)
}

class ResourceFacts {
  identity: ResourceIdentity
  github: GitHubRepository
  shape: ResourceShape
  canonicalPath: CanonicalPath
  branch: BranchName
  health: ResourceHealth
}

/// Context-authored prose from tracked AGENTS.pkl.
prose: NonEmptyString

/// Workbench supplies every remaining value from its explicit Subject and
/// participating repositories. The contract exposes no environment or
/// filesystem input.
subject: SubjectFacts
resources: Listing<ResourceFacts>
generatedPaths: Listing<CanonicalPath>
handOwnedPaths: Listing<CanonicalPath>
`

// Generated from ../../pkl/WorkbenchCommitPlan.pkl.
const localWorkbenchCommitPlanContract = `module phosphor.workbench.WorkbenchCommitPlan

typealias NonEmptyString = String(length > 0)
typealias PackageScope = String(matches(Regex(#"@[a-z0-9][a-z0-9._-]*"#)))
typealias GitHubRepository =
  String(matches(Regex(#"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+"#)), !endsWith(".git"))
typealias ResourceIdentity = PackageScope | GitHubRepository
typealias ChangeId = String(matches(Regex(#"[a-z0-9][a-z0-9._-]*"#)))
typealias RelativePath = String(length > 0, !startsWith("/"))
typealias HunkId =
  String(matches(Regex(#"[0-9a-fA-F]{4,64}(?::[0-9]+(?:-[0-9]+)?(?:,[0-9]+(?:-[0-9]+)?)*)?"#)))

class CommitSelection {
  title: NonEmptyString
  description: NonEmptyString
  filePaths: Listing<RelativePath> = new {}
  hunkIds: Listing<HunkId> = new {}
  unrelatedDeletedPaths: Listing<RelativePath> = new {}
}

changeId: ChangeId
summary: NonEmptyString
commits: Mapping<ResourceIdentity, CommitSelection>(length > 0)
`

// Generated from ../../pkl/WorkbenchSnapshot.pkl.
const localWorkbenchSnapshotContract = `module phosphor.workbench.WorkbenchSnapshot

typealias PackageScope = String(matches(Regex(#"@[a-z0-9][a-z0-9._-]*"#)))
typealias GitHubRepository =
  String(matches(Regex(#"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+"#)), !endsWith(".git"))
typealias ResourceIdentity = PackageScope | GitHubRepository
typealias CanonicalPath = String(length > 0, !startsWith("/"))
typealias CommitId = String(matches(Regex(#"(?:[0-9a-f]{40}|[0-9a-f]{64})"#)))

class PackageScopeShape {
  kind: "packageScope" = "packageScope"
  scope: PackageScope
}

class RepositoryShape {
  kind: "repository" = "repository"
}

typealias ResourceShape = PackageScopeShape | RepositoryShape

class SnapshotResource {
  shape: ResourceShape
  github: GitHubRepository
  canonicalPath: CanonicalPath
  commit: CommitId
}

/// Snapshot revisions are independent of the Subject's branch policy. The map
/// key is the shape-derived identity; the value records immutable origin facts.
resources: Mapping<ResourceIdentity, SnapshotResource>(length > 0)
`

// Generated from ../../pkl/Verifier.pkl.
const localVerifierContract = `/// One registered diagnostics or artifact source.
///
/// A repository's ` + "`" + `verifiers/<id>/VERIFIER.pkl` + "`" + ` amends this module.
module phosphor.workbench.Verifier


/// Repository-relative: forward slashes, no leading slash, no ` + "`" + `..` + "`" + ` segment.
typealias RepoPath = String(!isEmpty, !startsWith("/"), !contains("\\"), !split("/").contains(".."))

typealias Name = String(matches(Regex(#"[a-z0-9][a-z0-9-]*"#)))

typealias Produces = "report" | "artifact"

abstract class Selector {
  fixed kind: String
  hidden defaultNoun: String
}

/// One Unit; inputs are every tracked path.
class WholeTree extends Selector {
  fixed kind = "WholeTree"
  hidden defaultNoun = "tree"
}

/// One Unit; inputs are the tracked paths matching ` + "`" + `patterns` + "`" + `.
class Paths extends Selector {
  fixed kind = "Paths"
  hidden defaultNoun = "selection"
  patterns: Listing<RepoPath>(!isEmpty, isDistinct)
}

/// The Verifier's ` + "`" + `command` + "`" + `, run with ` + "`" + `VERIFY_ROLE=select` + "`" + `, enumerates its own Units.
class Own extends Selector {
  fixed kind = "Own"
  hidden defaultNoun = "unit"
}

abstract class Toolchain {
  fixed kind: String
}

/// Incremental through Units and the action cache alone.
class StrongToolchain extends Toolchain {
  fixed kind = "strong"
}

/// Transitional: files matching ` + "`" + `retained` + "`" + ` under /scratch survive between Runs of one Unit.
class Weak extends Toolchain {
  fixed kind = "weak"
  retained: Listing<RepoPath>(!isEmpty, isDistinct)
  auditRate: Number(isBetween(0, 1)) = 0.1
}

hidden Strong: Toolchain = new StrongToolchain {}

id: Name

/// Pinned by digest.
image: String(matches(Regex(#"[^@\s]+@sha256:[0-9a-f]{64}"#)))

/// Repository paths copied into /verifier, relative to their longest common directory.
program: Listing<RepoPath>(isDistinct)

command: Listing<String>(!isEmpty)

selector: Selector

displayNoun: String(!isEmpty) = selector.defaultNoun

produces: Produces

/// Artifact only: paths under /out that form the Artifact.
outputs: Listing<RepoPath>(isDistinct, (produces == "artifact") == !isEmpty)

/// Report only: the Report enumerates sub-Unit members.
members: Boolean(!this || produces == "report") = false

/// Mount name to Verifier id; mounted read-only at /upstream/<name>/.
requires: Mapping<Name, Name>

toolchain: Toolchain = Strong

deterministic: Boolean = true

/// ` + "`" + `none` + "`" + ` runs the container without a network; ` + "`" + `unrestricted` + "`" + ` gives it the default network.
typealias Network = "none" | "unrestricted"

network: Network = "none"

timeout: Duration(isPositive) = 5.min

output {
  renderer = new JsonRenderer {
    converters {
      [Duration] = (duration) -> duration.toUnit("s").value
    }
  }
}
`

// Generated from ../../pkl/Focus.pkl.
const localFocusContract = `/// What a ` + "`" + `verify` + "`" + ` call attends to. Every listing narrows; an empty listing is unrestricted.
module phosphor.workbench.Focus

typealias Severity = "error" | "warning" | "suggestion" | "message"

class Focus {
  /// Globs over repository-relative paths.
  files: Listing<String(!isEmpty, !startsWith("/"))>(isDistinct)
  /// Diagnostic codes or error keys.
  codes: Listing<String(!isEmpty)>(isDistinct)
  /// Verifier ids.
  verifiers: Listing<String(!isEmpty)>(isDistinct)
  /// Unit ids.
  units: Listing<String(!isEmpty)>(isDistinct)
  /// Sub-Unit member names, such as test names.
  members: Listing<String(!isEmpty)>(isDistinct)
  severities: Listing<Severity>(isDistinct)
}
`
