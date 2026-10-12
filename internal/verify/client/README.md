# Verify presentation

`Fold` annotates occurrences with delta status, simplifies families before
Focus filters attention, and builds `Delta.Output`. Text and default JSON read
that same grouped value, including the error key in JSON and the text header.
Messages render below the header unless identical to the error key, and blank
lines stay empty. Last Seen retains original messages and positions so
resolved occurrences join their family. Legacy sessions lacking messages use
the error key until refreshed. `--all` reads Last Seen to retain actual status
and never writes it; `--reset` explicitly clears it first.

Unknown counts reflect knowledge: individually unknown Units retain IDs and
numeric counts. A failed selector uses `units: null, scope: "all"`; the known
Unit total excludes that Verifier and names it under `unknownVerifiers`.
Action keys and full Observation details are available through
`--json=observation`; default JSON contains only the grouped public reading.

`GroupedRender` and `GroupedJSON` snapshot actual `Present` output across the
released attention cases and template/status, unknown, Focus, Gate, fileless,
empty-message, and legacy-session cases. `DeltaCommutesWithGrouping` checks
that status and Focus do not fragment or change a template. The released
attention inputs and expected state transitions remain in `testdata/delta.json`.

To deliberately record reviewed output changes:

```sh
mise exec -- go test ./internal/verify/client -run 'GroupedRender|GroupedJSON' -update-grouped
```

The bun generator regenerates only the Bun.Glob parity matrix. The TypeScript
Delta implementation was removed after the 0.9.0 release; its frozen fixtures
remain authoritative for attention semantics. Simplifier parity generation
lives under the sibling simplify package and runs the current TypeScript
simplifier itself.
