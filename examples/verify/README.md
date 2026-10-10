`workbench.pkl` ports the reference demo declaration to Workbench's Pkl types.
The verifier programs and image builds belong to the verifier catalogue; this
example contains declarations only.

`demo/` contains unchanged `workbench.pkl` and `VERIFIER.pkl` fixtures from
`tools/verify/demo/repo/`; `reference/` snapshots `tools/verify/schema/` from the
verify rewrite reference. The Pkl package tests evaluate these fixtures against
both schema directories and require byte-identical Verifier JSON, including
property order, defaults, and duration rendering. Keep the reference snapshots
unchanged when updating Workbench's contracts.

`reference/LegacyRepository.pkl` snapshots Workbench’s Repository contract before
this extension, so compatibility tests can compare the existing fields.
