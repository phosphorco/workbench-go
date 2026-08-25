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

// Generated from ../../pkl/Repository.pkl.
const localRepositoryDeclarationContract = `module phosphor.workbench.Repository

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
