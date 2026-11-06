# Workbench 0.10.0 release candidate

This candidate packages the 0.10.0 Workbench binary and the 0.10.0 Pkl contract. It is not a statement that either has been published. The published 0.9.1 binary continues to use the immutable 0.9.0 contract.

The candidate adds an optional preparation step to the reusable buildable promotion workflow and opt-in `observed-v1` build input binding. The binding detects changes observed between build and seal and checks bound output and source identities at consumption. It does not authenticate provenance or guarantee an immutable source snapshot.

The release workflow verifies and packages four native platforms and the contract, checks the candidate assets, and publishes one verified draft on the matching tag. Public acceptance follows publication: it checks the released binary revision and exercises the bound build lifecycle, while preserving published historical compatibility checks. A failed public acceptance requires investigation of the existing release before any retry; it does not undo publication.
