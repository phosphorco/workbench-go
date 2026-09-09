---
root: true
docs:
  - files: ["README.md"]
    message: "Keep the project context boundary visible."
commands:
  - files: ["cmd/**/*.go"]
    command: "go test ./cmd/..."
    label: "verify command changes"
---

The markdown body is documentation for the human and is not a contribution.
