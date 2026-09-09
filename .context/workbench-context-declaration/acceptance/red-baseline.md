# Context declaration public red baseline

Captured 2026-09-09 against the frozen current binary supplied by root, before
Pkl production changes:

```text
binary: /tmp/workbench-context-pre-pkl
sha256: 5ce415587e26027957e19f3925b11592e95399cc3cfce1a42ea510528bd8bf5b
```

The public grammar used by the initial fixtures is:

```pkl
amends "workbench:context"

scope = "subtree"

contributors {
  ["project-guidance"] = new AiContext {}
}
```

The project declaration is `workbench-context.pkl`; the home contract URI is
`workbench:context-home`.

The acceptance instrument is compiled and executed with:

```text
WORKBENCH_CONTEXT_DECLARATION_BINARY=/tmp/workbench-context-pre-pkl \
  go test ./acceptance -run TestContextDeclarationPublicWitnesses -count=1 -timeout 120s
```

It fails only behavioral assertions, after compiling successfully:

```text
explicit_builtin_Pkl_declaration_activates_the_real_hook
  Pkl builtin declaration did not activate guidance: stdout=""

legacy_JSON_alone_is_inert_even_with_matching_guidance
  legacy JSON was not inert: err=<nil>
  stdout="{\"hookSpecificOutput\":{\"hookEventName\":\"PostToolBatch\",\"additionalContext\":\"Legacy JSON activation must be inert.\"}}"
  stderr=""

disabled_nearest_declaration_blocks_ancestor_selection
  ancestor selection crossed nearest disabled boundary: err=<nil>
  stdout="{\"hookSpecificOutput\":{\"hookEventName\":\"PostToolBatch\",\"additionalContext\":\"Legacy ancestor selection must be blocked.\"}}"

empty_nearest_declaration_blocks_ancestor_selection
  ancestor selection crossed nearest empty boundary: err=<nil>
  stdout="{\"hookSpecificOutput\":{\"hookEventName\":\"PostToolBatch\",\"additionalContext\":\"Legacy ancestor selection must be blocked.\"}}"

directory-only_nearest_declaration_blocks_ancestor_selection
  ancestor selection crossed nearest directory-only boundary: err=<nil>
  stdout="{\"hookSpecificOutput\":{\"hookEventName\":\"PostToolBatch\",\"additionalContext\":\"Legacy ancestor selection must be blocked.\"}}"

init_creates_the_project_Pkl_declaration
  init did not create <temp>/workbench-context.pkl: no such file or directory
  stdout="{\"path\":\"<temp>/.workbench/context.json\",\"optIn\":true,\"changed\":true}\n"

init_ignores_legacy_JSON_and_preserves_it
  init with legacy JSON did not create Pkl declaration: no such file or directory
  stdout="{\"path\":\"<temp>/.workbench/context.json\",\"optIn\":true,\"changed\":false}\n"
```

The no-source witness passes as required: exit 0, stdout/stderr empty, and no
home declaration, runtime, socket, lock, cache, Pkl, or legacy JSON artifact.
That passing control is retained to guard the inactive fast path while the
other subtests provide the cutover reds.

The instrument also includes independent all-Pkl ownership controls. Each
control has an enabled ancestor declaration and a nearer disabled, empty, or
directory-only declaration. It asks `status` at the ancestor to report
`enabled`, then asks `status` at the descendant to report `inactive`, with the
nearer declaration's canonical root and blocking reason, before exercising the
hook. Against the frozen binary these fail at the parent status assertion with
the behavioral result `inactive` / `no applicable project or home opt-in
declaration`; this is the expected pre-Pkl red, not a compile or unresolved
symbol failure. Every hook subtest registers cleanup scoped to its isolated
socket identity.

The representative frozen status result is `activation.state = "inactive"`,
with an empty `activation.scope.canonicalRoot` and the first reason
`activation.inactive: no applicable project or home opt-in declaration`. The
expected post-cutover status is the opposite public witness: the ancestor
reports `enabled` with its canonical root, while the child reports `inactive`
with the child `workbench-context.pkl` canonical root and a disabled, empty, or
directory-coverage blocking reason. This is why the all-Pkl controls are not
accepted merely because the frozen hook is silent.

The initial frozen run after these ownership controls reports 10 behavioral
failures: Pkl activation, legacy JSON activation, three legacy-fallback
boundary cases, three enabled-Pkl-parent status cases, and both init cases.
