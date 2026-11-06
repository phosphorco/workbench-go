package setup

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
