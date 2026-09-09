# Warm-cache evidence

Command:

```text
timeout --kill-after=5s 120s bash .context/workbench-context-declaration/acceptance/warm-cache/warm_cache_probe.sh
```

The probe used the installed binary and exact private runtime layout:

```text
binary       /tmp/wctx-install.6uobrla_/bin/workbench
private Pkl  /tmp/wctx-install.6uobrla_/libexec/workbench/pkl
runtime lock /tmp/wctx-install.6uobrla_/share/workbench/runtime-lock.json
harness      claude (PostToolBatch / Read payload)
```

Verified SHA-256 values were, respectively, `a4200299ca9bfe76477ec071e9d7df42c3ba01c9b6451925940cbdc7d1e1905c`, `3180b62da95c0cad1d904e9bb6c5f4a8f9032413c21e53194bb91ff1ee5f3211`, and `6f2a3df238ba7bc7e2210e7a26eb1ccac7556dbd9310b73f9159b95be59eb82c`.

Workload: one home Pkl declaration, one project Pkl declaration importing local `rules.pkl`, and bounded hook inputs. The home policy set `idleTTLMs = 200` ms. Counted runs used `strace -f -e trace=execve` and counted exact private-Pkl `execve` entries; timing runs were untraced and therefore do not include strace overhead.

```text
cold:          2 workers (home + project; local import in project worker)
warm 1..3:     0, 0, 0 workers (daemon had restarted after 200ms idle)
edit true->false: 1 worker, silent inactive selection
edited warm:   0 workers
restore false->true: 1 worker, guidance restored
restored warm: 0 workers
```

Twenty untraced post-restore hooks while targeting a warm daemon measured, sorted in milliseconds:

```text
9,9,9,10,10,10,10,10,10,10,10,10,10,10,10,10,10,11,11,24
p95 = 11 ms
```

Every invocation had an external timeout; cleanup matched the exact daemon argv/socket identity and left no fixture daemon. Raw `strace` and stdout/stderr evidence is retained at `/tmp/workbench-context-warm-traces.0e6lXY`.
