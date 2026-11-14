# Workbench 0.12.1

Fix explanation-history writes at the shared cache's file-count or byte limit.
After rotating an old trace block, admission now includes both the durable
rotation-state replacement and the incoming block before eviction stops. The
previous loop could stop when only the incoming block fit, then refuse the
larger rotation write even though more owned blocks could be rotated safely.

This fixes the intermittent `capacity exceeded` / `pending batch could not be
written` error observed by BB's Context Magnet. It does not clear history,
increase the cache limits, evict unrelated snapshots or change context delivery.
Normal oldest-block retention still applies and historical gaps remain visible.
Tests cover the shared entry limit, byte limit, repeated rotation, retained
identities across reopening and preservation of unrelated cached data.

This is a binary-only patch on 0.12.0. The Pkl contract remains 0.12.0 at its
original download coordinates; no declaration migration is required.
