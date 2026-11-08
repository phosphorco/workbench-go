# Workbench 0.11.0 release candidate

This source candidate packages the 0.11.0 Workbench binary and Pkl contract; it does not establish that either has been published. The published 0.10.0 contract and its historical compatibility checks remain unchanged.

A buildable may opt into `host-observed-v1` local execution alongside `observed-v1` input binding. This permits a validated unsealed local candidate for the current host after a build. It detects observed input and output changes; it does not authenticate provenance, guarantee an immutable snapshot, or detect changes reversed between checks. Full sealing and promotion still require every declared platform.

The release workflow builds and checks four native platforms and the contract before tag-only publication. Public acceptance follows publication and exercises the host-local opt-in on Linux x86_64; it does not establish that behavior on the other three platforms. A failed public acceptance requires investigation of the existing release before any retry; it does not undo publication.
