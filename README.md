# Workbench

**Reconcile one local development environment. Preserve every repository as its own Git authority.**

Workbench is Phosphor’s local desired-state reconciler for development environments.

A developer or agent chooses:

- the repositories that begin a line of work;
- the shared branch name for that work;
- the base branch from which missing branches should begin.

Workbench then assembles the complete repository closure, checks out that work line across every participating repository, derives the package and TypeScript graph, projects the appropriate agent skills, installs or links dependencies, and verifies that the resulting environment is coherent.

The result feels like a purpose-built monorepo without requiring Phosphor’s source to live in one Git repository.

## Install the released Workbench with Mise

Workbench releases contain a self-contained executable plus private, pinned Pkl
and Bun runtimes. Mise discovers the native macOS or Linux archive directly
from the public GitHub backend identity:

The archive licenses first-party Workbench code under Apache-2.0 and carries
the independently governed upstream license, notice, patent, and provenance
inventory for Go, Pkl, Bun, pkl-go, msgpack, tagparser, and yaml.v3. Bundling those
components does not relicense them as Workbench code.
The pinned yaml.v3 license artifact carries its upstream MIT and Apache-2.0
terms together and is included unchanged in every platform archive.

The commands below describe the `0.5.0` release candidate. They become a public
installation path only after the matching tag, archives, contracts, checksums,
attestations, and immutable GitHub release have been published.

```sh
mise use -g github:phosphorco/workbench-go@0.5.0
workbench version
```

A released archive reports one compatible version and exact source revision.
The binary resolves its private runtimes relative to its installed executable; a
released build never falls back to `PATH` or a source checkout. GitHub Actions
builds the four macOS/Linux ARM64/x64 archives, checksums, runtime inventory,
and attestations from the release tag. Workbench itself is not a general
release-publication manager.

## A Workbench is a disposable local environment for one line of work

An engineer may work on three unrelated projects by creating three separate Workbench directories. Each begins with a small context template and acquires only the repositories needed for its current Subject.

The assembled Workbench is intentionally malleable:

- `pkg/` and `repos/` are ignored by the outer context repository.
- Each checkout inside those directories remains fully tracked by its own Git repository.
- Generated workspace files are ignored and reproducible.
- The current subject is local and replaceable.
- The entire workbench may be discarded without discarding committed work.

**The outer repository tracks the working context, not the assembled source.**

## Four inputs own four kinds of meaning

| Input | Owns |
| --- | --- |
| Context template | Guidance, base tools, Mise configuration, and outer ignore rules. Different engineering or product contexts may use different templates without changing Workbench semantics. |
| `workbench-subject.pkl` | The current entrypoints and intended work line. It is local, ignored, and is the sole desired-state authority for the assembled Workbench. |
| Resource repositories | Source, independent Git history, repository-closure composition, skill declarations, and non-derivable package or publication policy. |
| `workbench-go` | Released Pkl contracts, resource validation, closure, identity, placement, branch reconciliation, hydration, skill projection, and safe application. |

The context template is a Phosphor convention, not a participating repository.

## The outer repository ignores the assembled Workbench

A typical workbench directory looks like this:

```text
<workbench>/
├── .git/
├── README.md
├── AGENTS.pkl
├── mise.toml
├── mise.lock
├── .gitignore
│
├── workbench-subject.pkl       # local desired state; ignored
├── AGENTS.md                   # generated orientation; ignored
├── .workbench/                 # plans, receipts, and local state; ignored
│
├── package.json                # generated root projection; ignored
├── tsconfig.json               # generated root projection; ignored
├── pkg/                        # ignored by the outer repository
│   └── @workbench-entry/       # independent PackageScope Git repository
│       ├── app/                # one declared package
│       └── tool/               # another declared package
└── repos/                      # ignored by the outer repository
    └── workbench-fixture-library/ # independent Repository Git repository
```

Ignoring `pkg/` and `repos/` prevents the context repository from accidentally treating nested checkouts as its files. It does not weaken the Git history or status of those repositories.

Workbench-owned generated files inside a resource repository must likewise be excluded from that repository’s commits.

## The Subject names the desired repositories and work line

`workbench-subject.pkl` is the local request for what should exist:

```pkl
amends "package://github.com/phosphorco/workbench-go/releases/download/0.5.0/workbench@0.5.0#/WorkbenchSubject.pkl"

workLine {
  branch = "cole/example-work"
  baseBranch = "main"
}

entrypoints {
  "https://github.com/phosphorco/workbench-fixture-entry"
}
```

The Subject contains:

- one or more entry repository designations;
- one branch name shared across the participating repositories;
- one base branch used when that work branch does not yet exist.

The entrypoints begin discovery. They receive no power to override, suppress, or reinterpret downstream repository declarations.

Running:

```sh
workbench setup
```

means:

> Make the local Workbench agree with `workbench-subject.pkl` wherever Workbench can do so without destroying Git-owned state.

Adding or removing an entrypoint means editing the Subject and running `workbench setup` again.

### A branch is a collaboration line, not a Workbench Snapshot

A handoff consists of:

```text
entrypoint or entrypoints
+ shared branch name
+ base branch
```

For example:

```text
entrypoint: phosphorco/workbench-fixture-entry
branch:     cole/example-work
base:       main
```

Another developer can construct that Subject and run setup. Workbench will assemble
`workbench-fixture-entry`, discover its declared `workbench-fixture-library`
include, and place both on the named branch.

This reproduces a named line of collaboration. It does not reproduce an immutable revision set because branch names may move.

Exact reconstruction uses a separate Workbench Snapshot:

```text
resource identity → exact commit SHA
```

Run `workbench snapshot record` to write the exact immutable revision set, and
`workbench snapshot reproduce <file>` in a fresh context to acquire it. A
snapshot never changes the Subject’s branch policy and never resets or rewrites
an existing checkout; a conflicting checkout stops reproduction in recoverable
state.

Workbench 0.5 records `WorkbenchSnapshot.pkl` at
`.workbench/workbench-snapshot.pkl` by default. A 0.5 binary always writes that
current contract, even when the Subject amends an older released contract.
Explicit reproduction can still read the exact snapshot contracts released by
Workbench 0.2.0, 0.3.0, and 0.4.0 through version-scoped compatibility adapters; it
does not rename or delete a user-authored snapshot.

### There is no second branch lock

A separate `branch-name.txt` would duplicate the Subject. An “unlock” would make the branch invariant optional and create two competing sources of truth.

The lawful way to change branch policy is to change `workbench-subject.pkl`.

Workbench cannot prevent arbitrary filesystem edits without mediating the filesystem itself. It instead enforces branch coherence at the operations it owns:

- setup reports a branch mismatch as unhealthy;
- commit refuses to include a modified repository on the wrong branch;
- push occurs only for commits created under an explicit commit plan;
- generated `AGENTS.md` tells agents which branch governs the participating repositories.

A filesystem lock, watcher, or permission layer is outside this design.

## Repository `includes` construct the repository closure

Each participating resource contains a root `workbench.pkl`:

```pkl
amends "package://github.com/phosphorco/workbench-go/releases/download/0.5.0/workbench@0.5.0#/PackageScopeRepository.pkl"

scope = "@workbench-entry"

includes {
  ["phosphorco/workbench-fixture-library"] {
    skills {
      editing {
        domains = Set("engineering")
      }
    }
  }
}
```

`includes` means:

> This repository must participate in every Workbench containing this resource.

It is not equivalent to a `package.json` dependency.

An include may exist because the assembled Workbench needs:

- unpublished source;
- cross-repository development;
- build tooling;
- agent skills;
- code generation inputs;
- another non-package resource.

Package imports form a separate graph after the repository closure exists.

Requirements are recursive:

```text
RepositoryClosure = leastClosure(Subject.entrypoints, Resource.includes)
```

The same identity and designation reached twice is a no-op. Workbench stops when:

- one identity is reached through conflicting designations;
- one designation claims incompatible identities;
- a canonical path is occupied by another identity.

Repository topology remains repository-owned. Workbench has no central repository registry, so private topology need not appear in public tooling or public context templates.

## Resource shape derives identity and placement

Resource authors do not declare a redundant generic Workbench identity.

Workbench initially supports a closed, versioned set of resource shapes:

```text
ResourceShape = PackageScope | Repository
```

For each shape, Workbench defines:

- how identity is derived;
- how conflicts are detected;
- where the checkout is placed;
- which policy schema applies.

For example:

```text
PackageScope("@phosphorco")
  identity  → package scope @phosphorco
  placement → pkg/@phosphorco

Repository("phosphorco/some-plugin")
  identity  → repository phosphorco/some-plugin
  placement → repos/some-plugin
```

A Repository-shaped `workbench.pkl` amends the released `Repository.pkl`
contract and does not author a name, scope, or generic identity:

```pkl
amends "package://github.com/phosphorco/workbench-go/releases/download/0.5.0/workbench@0.5.0#/Repository.pkl"

includes {}
packages {}
```

Its normalized GitHub acquisition designation supplies identity; the repository
name supplies the `repos/<name>` placement. Repository package placement remains
the distinct, versioned law of that shape.

A PackageScope resource is different: its checkout is a namespace container,
not a package root. Every `packages` key in the `0.5.0` contract is exactly
`<scope>/<leaf>`, and the leaf derives the only canonical child location:

```pkl
scope = "@workbench-entry"

packages {
  ["@workbench-entry/app"] {}
  ["@workbench-entry/tool"] {}
}
```

```text
pkg/@workbench-entry/             # independent Git/resource root
├── workbench.pkl
├── skills/                       # Git-owned resource skill sources
├── .agents/skills/               # ignored resource editing projections
├── app/
│   ├── src/                      # @workbench-entry/app source
│   ├── package.json              # generated package projection
│   └── tsconfig.json             # generated package projection
└── tool/
    ├── src/                      # @workbench-entry/tool source
    ├── package.json              # generated package projection
    └── tsconfig.json             # generated package projection
```

Workbench does not guess `src/`, `packages/<leaf>/src`, or another convenient
layout. A missing canonical child or any competing package layout stops setup
before generated workspace, TypeScript, skill, orientation, dependency, or
receipt outputs change. Adding a second package therefore cannot relocate the
first one. Skills, includes, branch health, orientation, and Git authority stay
at the resource root; only package-specific source and generated package files
live below the derived child directory.

Composite TypeScript build metadata is emitted beneath each package's generated
`dist/` tree, so repositories ignore one owned output tree rather than enumerating
package-specific `tsconfig.tsbuildinfo` files.

New shapes require a `workbench-go` release. “Extensible” means that adding an internal shape is a localized implementation change, not that resource authors may install arbitrary resource plugins.

## Setup reconciles checkouts and generated state

The central model is:

```text
Subject          = entrypoints + intended work line
RepositoryClosure = closure(Subject.entrypoints, repository includes)
DesiredCheckouts  = place every participating repository on Subject.workLine
DesiredFiles      = hydrate(RepositoryClosure, source facts, declared policy)
ChangeSet        = compare(Observed, Desired)
setup            = apply(Subject grant, ChangeSet)
```

`workbench setup`:

1. Evaluates the local Subject against the released Pkl contract.
2. Resolves every entrypoint and recursively computes the source closure.
3. Derives each resource’s identity and canonical placement.
4. Clones checkouts that are absent.
5. Fetches the remote refs needed to inspect the declared work line.
6. Checks out an existing subject branch or creates it from the declared base.
7. Observes source imports, structure, resource policy, and skills.
8. Computes the desired generated files and workspace links.
9. Applies only changes covered by Workbench’s setup authority.
10. Runs the required dependency and linking reconcilers.
11. Plans again and succeeds only when the owned projections have converged.

`.workbench/managed-checkouts.json` records the checkouts Workbench created and
the provenance needed for orphan reporting and guarded prune. When setup finds
generated receipt state from an older release, it validates all identities,
paths, ownership facts, and any simultaneous current receipt before doing
filesystem or Git work. It installs and re-reads the current receipt before an
exactly proven legacy receipt may be retired. Malformed, ambiguous, foreign, or
disagreeing state causes a zero-change refusal, leaving a reachable manual
repair path instead of guessing ownership.

The public workflow remains centered on `setup`. Observation, planning, and comparison are internal machinery, though a future `status` or `setup --check` may expose their evidence.

This gives Workbench Terraform-like desired state without requiring a separate public plan-and-apply lifecycle.

## Branch coherence is explicit but non-destructive

Every participating checkout is intended to use the Subject branch.

When a checkout is missing, setup may:

- clone it;
- fetch remote refs;
- check out the remote subject branch when it exists;
- create a local subject branch from the declared base when it does not.

When a checkout already exists, setup may switch it only when Git can preserve its complete state.

Setup stops rather than:

- switching a dirty checkout to another branch;
- overwriting conflicting untracked files;
- resetting or rewriting a branch;
- rebasing or merging;
- discarding staged or unstaged changes;
- force-updating a ref;
- guessing which branch the operator intended.

The base branch is used to create a missing subject branch. It does not give Workbench authority to rebase or reset an existing subject branch when the base later moves.

A healthy Workbench satisfies three separate conditions:

```text
Healthy =
  branchCoherent(RepositoryClosure, Subject.workLine)
  and projectionConverged(RepositoryClosure)
  and externalReconcilersHealthy(RepositoryClosure)
```

`projectionConverged` applies only to Workbench-owned declarative files. Package-manager caches, downloads, `node_modules`, and platform-specific tool state use their own health contracts rather than being compared byte-for-byte as declarative state.

## Source derives adjacency; Pkl declares irreducible semantics

Workbench derives facts that source can prove.

For TypeScript packages, source imports may determine:

- package adjacency;
- workspace membership;
- internal `workspace:*` links;
- source, test, and tool project references;
- common runtime versus test-only dependency placement.

Source cannot safely determine every package semantic. Pkl remains responsible for facts such as:

- peer dependencies;
- optional dependencies;
- published version requirements;
- required-but-unreferenced dependencies;
- command-line entrypoints;
- extension metadata;
- exceptional build or publication policy.

The governing rule is:

> Source derives dependency adjacency. Pkl declares the dependency semantics that source cannot prove.

## Workbench owns complete generated outputs

Generated files use whole-file ownership at the filesystem boundary.

Humans supply semantic input through source and typed Pkl values. Workbench renders the complete output:

```text
DesiredFile =
  render(
    derivedSourceFacts,
    declaredExceptionalPolicy
  )
```

Workbench does not preserve arbitrary unknown fields from an existing generated file. Doing so would create merge, deletion, precedence, and normalization states that could not converge reliably.

For a VS Code extension, resource policy might declare:

```pkl
vscodeExtension {
  publisher = "phosphor"

  engines {
    vscode = "^1.100.0"
  }
}
```

Workbench then places those values in the generated `package.json`. Fields derived by Workbench cannot be overridden through an untyped escape hatch. Conflicting semantic inputs produce an error.

Workbench may own projections such as:

- root and package `package.json` files;
- root, aggregate, source, test, and tool `tsconfig` files;
- package exports and imports;
- workspace dependency declarations;
- package-manager catalogs and workspace membership;
- selected, Workbench-owned `.agents/skills/<skill-name>` projection trees;
- dependency installation and workspace links.

Every generated output must be removable and reproducible. Commit tooling must reject Workbench-owned generated paths from source commits.

The existing `workspaces-sync-go` work contributes two proven ideas:

1. Pkl is evaluated as typed, capability-constrained configuration.
2. Repository mutation is represented first as a deterministic change set.

The final Workbench implementation absorbs repository observation, planning, and granted application into Go. A deterministic patch may remain available as an inspectable representation of the plan, but the internal JSON subprocess boundary need not survive.

## Skills follow the repository closure

Skills have two independent properties:

- each skill declares one domain: `orchestration`, `engineering`, or `general`;
- the consuming resource selects where imported skills should be visible.

Resource repositories author Git-owned skill sources only under
`skills/<skill-name>/**`. Workbench discovers the assembled inventory from
those source trees; it never treats a generated `.agents/skills` projection as
source. This keeps export and import independent: a repository can export one
skill from `skills/` while receiving a different editing skill under
`.agents/skills/` without colliding with itself.

`editing` exposes selected skills while changing the consuming resource. `workbench` exposes them at the workbench root.

```pkl
skills {
  editing {
    domains = Set("engineering")
    names = Set("mvvm", "view-model-interfaces")
  }

  workbench = "all"
}
```

Domain and name roots combine by union. Workbench then follows each selected skill’s explicit composition edges.

Resource authors select semantic roots. They do not order traversal or copy transitive skill dependencies.

For `editing`, Workbench projects the selected roots and their explicit
composition closure into the consuming resource's
`.agents/skills/<skill-name>/**`. For `workbench`, it projects that same closure
at the Workbench root. Each projected skill subtree is a recorded
whole-output-owned artifact. Setup preserves unrelated context-owned sibling
skills, refuses a selected-name collision it does not own, and removes only a
stale subtree whose prior Workbench ownership and bytes are still proven.
Resource repositories that receive editing projections ignore
`.agents/skills/`; Git continues to own `skills/`.

Because skill sources are assembled on the Subject branch, agents receive the skill definitions associated with the same collaboration line as the source they are editing.

### One Skill Catalog owns checking, selection, and projection

Workbench parses skill sources into one Skill Catalog. The Catalog is the only
owner of skill metadata, Markdown links, composition edges, diagnostics,
selection, and projection facts; setup does not carry a second, weaker
interpretation.

To check the context-owned catalog in the current directory without changing
any file, run:

```sh
workbench skills check
```

The command intentionally has no path flag. It checks `.agents/skills` in the
current directory and reports the derived skill and composition-edge counts.
Each skill is one flat `.agents/skills/<name>/SKILL.md` directory. YAML
frontmatter must name the folder and declare one of the three domains.
Workbench validates local Markdown targets, explicit composition labels,
skill-name references, and optional description-marker metadata. Blocking
diagnostics identify the source-relative file and line and produce a nonzero
exit. Warnings remain visible in the successful report without making an
otherwise valid catalog fail.

`workbench setup` applies the same Catalog to every participating resource's
Git-owned `skills/` tree before canonical checkout reconciliation or
generated-path mutation. Scratch acquisition is read-only with respect to the
Workbench and binds validation to the exact revision later reconciled.
Only a valid batch may proceed to selection and projection. This makes the
explicit check and setup preflight two consumers of the same parsed facts,
not two linters that can disagree.

Projection copies selected skill subtrees byte-for-byte into
`.agents/skills/`; it does not rewrite authored paths. A composition link to a
peer skill remains portable because the explicit dependency is projected as a
sibling. A relative link that resolves only through repository files outside
the projected skill closure is refused before the first projection write.
Resource authors should keep projected references inside the selected skill
subtrees and their explicit composition dependencies.

## `AGENTS.pkl` turns current Workbench state into agent orientation

Workbench `0.5.0` retains the constrained `AgentInstructions.pkl` contract.

The context template tracks `AGENTS.pkl`:

```pkl
amends "package://github.com/phosphorco/workbench-go/releases/download/0.5.0/workbench@0.5.0#/AgentInstructions.pkl"

prose = """
# Agent instructions

Work inside the assembled Workbench.
"""
```

During setup:

```text
AGENTS.pkl
+ deterministic Subject and repository-closure summary
→ generated AGENTS.md
```

The generated summary may include:

- the Subject branch and base;
- entrypoints;
- composed resource identities;
- canonical checkout paths;
- branch health;
- the commands agents should use;
- warnings about generated and hand-owned paths.

`AGENTS.pkl` is tracked because it contains context-authored prose. `AGENTS.md` is ignored because it contains current local topology, which may be private and changes with the Subject.

The Pkl program receives only values explicitly supplied by Workbench. It does not read ambient environment variables, arbitrary files, or repository state directly.

Re-running setup is byte-convergent. Removing a participating repository removes its stale
identity, path, branch, and health facts from orientation without rewriting the
Git-owned `AGENTS.pkl` prose.

## `commit-plan.pkl` coordinates cross-repository work

Workbench must make it difficult for agents to lose work, commit unrelated edits, or push only part of an intended multi-repository change without noticing.

`commit-plan.pkl` describes one **Workbench Change Set**:

```pkl
amends "package://github.com/phosphorco/workbench-go/releases/download/0.5.0/workbench@0.5.0#/WorkbenchCommitPlan.pkl"

changeId = "fixture-cross-repository"
summary = "Exercise a cross-repository fixture change"

commits {
  ["@workbench-entry"] {
    title = "feat(fixture): consume the shared value"
    description = """
    Consume the library value through the workspace link.
    """

    filePaths { "app/src/index.ts" }
  }

  ["phosphorco/workbench-fixture-library"] {
    title = "feat(fixture): expose the shared value"
    description = """
    Expose the library value consumed by the entry package.
    """

    filePaths { "src/index.ts" }
  }
}
```

Each repository entry retains the existing Atomic Commit guarantee:

- exact files and hunks are selected;
- ambiguous pre-staged changes are refused;
- unnoticed tracked deletions are blocked;
- unrelated dirty work remains untouched;
- hooks run before the local commit is accepted.

The Workbench-level group is not transactionally atomic. Git hosts provide no transaction across repositories.

Execution is therefore a recoverable saga:

1. Evaluate the plan against its released, inert Pkl contract.
2. Verify that every selected repository belongs to the current repository closure.
3. Verify that every selected repository is on the Subject branch.
4. Reject generated paths, ambiguous staged state, invalid hunks, and unacknowledged deletions.
5. Preflight every repository before creating any commit.
6. Create exact local commits and record each resulting SHA.
7. Attach one shared Workbench change identifier to every commit.
8. Record the local commit group in a recovery ledger.
9. Attempt every push.
10. Report complete or partial remote state without rewriting successful commits.

If one push fails after another succeeds, Workbench records the partial state and supports a safe resume. It does not pretend to roll back the remote transaction.

The terms remain distinct:

- **Atomic Commit:** exact composition of one repository’s local commit.
- **Workbench Change Set:** a linked, recoverable group of repository commits.
- **Shared change ID:** the durable relationship between those commits.

Run `workbench commit` (or `workbench commit <plan>`) to begin or resume the
saga. The durable journal binds the newly evaluated exact plan to prepared
candidates, so retry cannot rebuild, rewrite, or duplicate a successful commit.

## Removing a repository from the closure does not delete it

When an entrypoint or include disappears:

```text
Repository closure ⊆ Present checkouts
```

Setup removes the repository from:

- workspace membership;
- dependency links;
- generated package and TypeScript graphs;
- projected skills;
- generated orientation.

It does not delete the checkout.

Workbench reports the checkout as orphaned. `workbench prune <identity>...`
may remove only a Workbench-created checkout after proving that it is clean,
pushed, unambiguous, non-nested, and independently recoverable. The command
re-observes every target before invoking its explicit deletion capability.
Ordinary setup never trades source preservation for tidiness.

## Ten laws define the program

1. **A Subject names entrypoints and one intended work line.** It is the sole local authority for desired Workbench state.

2. **Repository `includes` construct the least repository closure.** Entrypoints begin traversal but receive no override authority.

3. **Resource shape derives identity and placement.** Resource authors do not declare redundant generic identities.

4. **The outer context ignores the assembled Workbench.** Every nested checkout remains independently governed by its own Git repository.

5. **Setup reconciles explicitly declared branch state without destructive interpretation.** It may safely create or switch branches; it may not reset, rebase, merge, discard, or guess.

6. **Derive before declaring.** Source owns facts that can be recovered mechanically; Pkl owns irreducible semantics.

7. **Workbench owns complete generated paths.** Human-authored semantics enter through source or typed policy, not preserved fragments inside generated files.

8. **Successful setup produces branch coherence, projection convergence, and healthy external reconcilers.**

9. **Checkout preservation outranks automatic pruning.** Leaving an orphan is safer than deleting recoverable work.

10. **Cross-repository work is linked and recoverable, never transactionally atomic.**

## Failure is safer than guessing

Setup stops when it cannot preserve the laws. Examples include:

- an invalid Subject or resource specification;
- conflicting identity or designation claims;
- an occupied canonical path with the wrong identity;
- an unsafe branch switch;
- a missing or invalid base branch;
- a generated path containing hand-owned meaning;
- contradictory derived and declared package semantics;
- an absent selected skill;
- an invalid skill-composition graph;
- a failed dependency or linking health check;
- a generated projection that does not converge.

Commit execution additionally stops for:

- a selected repository outside the current repository closure;
- a modified repository on the wrong branch;
- ambiguous pre-staged changes;
- stale or invalid hunk identifiers;
- unacknowledged tracked deletions;
- selected Workbench-owned generated paths;
- a hook failure;
- a remote push rejection.

Diagnostics identify the resource, branch, path, or commit in conflict and leave existing source history recoverable.

## The 0.1.0 and 0.2.0 paths remain compatibility baselines

The first production slice crosses anonymous public boundaries to assemble two
dedicated, independently governed fixture repositories:

```text
Subject
└── phosphorco/workbench-fixture-entry (@workbench-entry)
    └── includes phosphorco/workbench-fixture-library (@workbench-library)
```

Local transport redirects and in-process repository fixtures cannot satisfy
this public boundary.

The local Subject names:

```text
branch:     workbench/proof-0.1.0
baseBranch: main
entrypoint: phosphorco/workbench-fixture-entry
```

The slice must prove that:

- the outer context ignores `workbench-subject.pkl`, `pkg/`, `repos/`, local state, and generated root projections;
- the immutable `workbench-go` release `0.1.0` supplies the typed Subject and resource contracts;
- the released Subject contract describes the entrypoint and work line;
- the released resource contract describes both real package-scope repositories;
- repository-owned `includes` construct the closure without a central registry;
- both repositories are cloned into `pkg/@workbench-entry` and `pkg/@workbench-library`;
- both repositories safely use the same Subject branch;
- a missing Subject branch is created from the declared base;
- a dirty checkout on another branch causes setup to stop with zero Git mutation;
- source and policy hydrate into one real package and TypeScript graph;
- one real cross-repository package typechecks through its generated workspace link;
- one domain-selected skill and its composition dependency are discoverable;
- running setup again changes no Workbench-owned projection;
- Git-owned source changes survive every setup action.

Release 0.2.0 preserves this accepted public path while completing the v1
lifecycle with generated orientation, recoverable Change Sets, exact historical
snapshots, explicit safe prune, the closed Repository shape, and self-contained
Mise distribution. Fixture evolution must first preserve the accepted 0.1.0
revisions on their canonical `workbench/proof-0.1.0` branches.

The immutable `0.2.0` contract also retains its historical single-package
PackageScope layout: a package may use the PackageScope checkout root as its
source root. Workbench selects that law from the exact released contract named
by the Subject and resource declarations. The immutable `0.3.0` release introduced the
nested `<leaf>/src` law rather than silently reinterpreting a `0.1.0` or `0.2.0`
contract. A compatibility run must therefore continue to assemble the permanent
`workbench/proof-0.1.0` anchors and the immutable `0.2.0` public path while the
new candidate proves nested PackageScope behavior independently.

The immutable `0.4.0` line introduced the current Workbench vocabulary and
the separation between Git-owned `skills/` sources and Workbench-owned
`.agents/skills/` projections. Workbench 0.5 preserves the 0.4 contracts and
public fixture branch while adding the first-class Skill Catalog check and
setup preflight. Those immutable fixture bytes predate the current catalog
frontmatter law, so 0.5 retains an explicit source-anchored refusal oracle
rather than rewriting or blessing them. Contract, snapshot, and declaration
compatibility remain independently covered; current positive setup proof uses
current resource declarations.

Adopting BasinDB and `phosphorco/community-packages` is a separate future
promise. Their Workbench declarations, repository split, migration, and history
extraction are not acceptance inputs for this slice.

## Deliberate non-goals

Workbench is not:

- a filesystem-level edit lock;
- a distributed Git transaction;
- a general-purpose plugin or arbitrary-hook framework;
- a requirement that each resource build after an isolated clone;
- a central registry of Phosphor repositories;
- a package-version or compatible-revision solver;
- a mechanism for placing multiple revisions of one identity in one Workbench;
- a manager for naming or deleting surrounding workbench directories;
- an automatic merge, rebase, or branch-reset system;
- an automatic checkout deletion system (deletion is explicit and guarded);
- a daemon or shared mutable coordination service;
- a release-publication manager;
- a repository-history migration tool.
- BasinDB or `phosphorco/community-packages` adoption, declarations, repository splitting, migration, or history extraction.

History extraction, hosting-provider migration, and generalized
release-publication management remain separate systems. They have different
authority, recovery, and failure boundaries from reconciling a local
development environment.
