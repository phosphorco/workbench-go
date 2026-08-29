<!-- Update from the repository root: UPDATE_WORKBENCH_SNAPSHOTS=1 go -C tools/workbench-go test ./acceptance -run '^TestWorkbenchOrphanNarrative$' -count=1 -->
# Workbench setup narrative

## Create the Subject

The Subject declares both the entry repository and the library repository.

```console
$ cat > workbench-subject.pkl <<'PKL'
amends "workbench-contract:/0.4.0/WorkbenchSubject.pkl"

workLine {
  branch = "local/package-scope-current"
  baseBranch = "main"
}
entrypoints {
  "https://github.com/phosphorco/entry"
  "https://github.com/phosphorco/library"
}
PKL
```

## Reconcile the Workbench

The first real setup creates the complete two-repository Workbench.

```console
$ workbench setup
Workbench reconciled 2 repositories; 11 generated paths changed.
```

## Edit the declaration

The Subject now declares only the entry repository.

```console
$ cat > workbench-subject.pkl <<'PKL'
amends "workbench-contract:/0.4.0/WorkbenchSubject.pkl"

workLine {
  branch = "local/package-scope-current"
  baseBranch = "main"
}
entrypoints {
  "https://github.com/phosphorco/entry"
}
PKL
```

## Reconcile the edited Subject

The second real setup reports the removed library as an orphan.

```console
$ workbench setup
Workbench reconciled 1 repository; 3 generated paths changed.
Orphaned checkout: phosphorco/library at repos/library
```

## Observe the preserved checkout

A real filesystem observation confirms that setup did not delete the orphan checkout.

```console
$ test -d repos/library && printf '%s\n' 'Preserved checkout: repos/library still exists.'
Preserved checkout: repos/library still exists.
```
