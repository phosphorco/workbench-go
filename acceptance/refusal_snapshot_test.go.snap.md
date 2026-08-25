<!-- Update from the repository root: UPDATE_WORKBENCH_SNAPSHOTS=1 go -C tools/workbench-go test ./acceptance -run '^TestWorkbenchDirtyOtherBranchRefusalNarrative$' -count=1 -->
# Workbench setup narrative

## Dirty checkout on another branch

Setup refuses the unsafe branch switch and leaves the complete Git and filesystem state unchanged.

```console
$ workbench setup
workbench: setup: reconcile canonical checkouts: plan checkout "<workbench>/pkg/@workbench-entry": dirty checkout is on branch "main", not Subject branch "local/package-scope-current"
exit 1
```
