# Pkl declaration diagnosis trial

On 2026-09-09, native Codex Luna (gpt-5.6-luna, xhigh, fast) completed a
528-second outcome-only diagnosis using the final installed Linux binary
`a4200299ca9bfe76477ec071e9d7df42c3ba01c9b6451925940cbdc7d1e1905c`.
The task was: “Find out why this project’s automatic guidance is not reaching
my coding session.” The repository README named Workbench and linked the public
guide; no route, command, expected diagnosis, or permission instruction was added
to the task. This was a known-product, repository-entry diagnosis trial.

The agent independently read the guide and declaration, ran human and JSON
`context status`, and correctly identified `contributors {}` as the primary
cause. It cited `provider.not-selected` and recommended explicitly selecting
`new AiContext {}`. This establishes discoverability of the declaration’s empty
selection and its explanation for this trial, not universal usability or a repair
trial. The project, guide, installed binary and hook configuration remained
byte-identical to their initial hashes.

The final answer also asserted a missing Codex installation. That secondary
claim is unsupported: the observer had installed hooks in the explicit isolated
CODEX_HOME, while the tool shell exposed a different isolated HOME. The agent’s
setup dry-run inspected that shell home. Preserve this as an instrumentation
confound and an overconfident inference; do not adopt it as a production defect
or describe the entire diagnosis as correct.

Native on-request approvals, workspace-write and user review were configured.
The sandbox’s namespace failure caused escalated requests; the observer manually
approved all 17 contained read-only commands without route advice. No commands
were denied, and no coaching was provided. This friction and observer time are
included in the elapsed duration. The fixture’s reviewed-hook trust bypass was
per-thread test instrumentation, not product trust automation. The trial does
not prove native user trust onboarding.

Raw, unedited evidence is under `/tmp/wctx-pkl-study._vl9275p/`: `events.jsonl`
contains the exact final answer and tool route; `approvals/` contains all requests
and decisions; `before-hashes.json`, `study.json`, `auth-cleanup.json`, and
`observer-cleanup.json` record configuration and cleanup. Native thread:
`01a084c9-48b0-7171-a5d9-8f5bf3d8c98d`.

Observer cleanup joined the app-server and removed the authorized temporary
same-user authentication copy. Original authentication metadata was unchanged;
credential contents were neither printed nor hashed. Exact fixture-process
inspection found no remaining processes. No global configuration was changed.

Separate controlled-endpoint native admission and performance proofs are in
[integration evidence](integration-evidence.md). They establish actual hook
admission, independently of this real-model diagnosis result.
