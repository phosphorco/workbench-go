[for=AGENT]

# Author a Pkl task graph

Save this as `feature.plan.pkl`, adapting the outcomes and oracles to the task:

```pkl
amends "workbench:plan"

meta { title = "Deliver an artifact"; goal = "Produce and review the agreed artifact." }

local api = new Action {
  id = "api"
  owner = "implementer"
  outcome = "Write artifact.txt with the agreed contents."
  grant { "artifact.txt" }
  oracle = module.mechanical("test -s artifact.txt")
}

local review = new Guard {
  id = "review"
  owner = "reviewer"
  outcome = "Review the artifact against the agreed contents."
  observes { "artifact.txt" }
  needs { module.produced(api); module.verified(api) }
  oracle = module.adjudicated("The artifact contains the agreed contents.", "reviewer")
}

nodes { api; review }
```

The example's file-size test only proves a nonempty artifact; replace it with
the real acceptance instrument. Use `workbench plan schema` for all fields and
helper signatures. For reusable fragments, import `workbench:plan` as `Plan`
and return typed `Plan.Action`, `Plan.Guard`, or `Plan.Selector` values. Inside
an amended definition, qualify helpers with `module.` as shown above.

For an effort with decisions, parallel consumers, and changing acceptance, use
the [campaign walkthrough](planning-tactics.md) and its complete
[discovery](../examples/bulk-export/discovery.plan.pkl),
[delivery](../examples/bulk-export/delivery.plan.pkl), and
[revised](../examples/bulk-export/revised.plan.pkl) definitions. They share
[typed node values](../examples/bulk-export/campaign.pkl); copy that local import
with the selected definition. Their application tests are illustrative commands
to implement in the target repository, not bundled passing fixtures.

Imports default to the plan's directory. Use `--module-root DIR` only when the
plan needs a wider local Pkl tree. Supply dynamic discovery as explicit Pkl
inputs; evaluation cannot access environment resources, networks, or arbitrary
repository files. Task planning does not require a Workbench Subject or setup.

## Release contract reference

The configured publication target is `{{.PklPackageURI}}#/Plan.pkl`.
Use it when inspecting the published schema outside the plan evaluator. This
reference is not a claim that the selected release has been published. The plan CLI
uses its embedded `workbench:plan` contract; replacing an `amends` line with a
release URI does not grant network access or change its supported contracts.
[/AGENT]
