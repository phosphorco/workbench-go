# Workbench contract references

[for=AGENT]
Read the amended contract before changing a declaration. The configured Pkl
publication base is `{{.PklPackageURI}}`:

| Declaration | Contract URI |
| --- | --- |
| Context Subject | `{{.PklPackageURI}}#/WorkbenchSubject.pkl` |
| Package-scope repository | `{{.PklPackageURI}}#/PackageScopeRepository.pkl` |
| Repository | `{{.PklPackageURI}}#/Repository.pkl` |
| Commit plan | `{{.PklPackageURI}}#/WorkbenchCommitPlan.pkl` |
| Snapshot | `{{.PklPackageURI}}#/WorkbenchSnapshot.pkl` |

Use these as publication references, not an instruction to upgrade existing
files. Preserve the version an existing declaration amends unless a contract
migration is part of the task. An exported URL does not establish that a release
exists or that the installed binary supports that version. The binary and Pkl
package have independent versions.
[/AGENT]
