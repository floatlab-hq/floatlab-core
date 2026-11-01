# Workload database replication

The pinned SQLite 3.50.4 `sqlite3_rsync` uses protocol 1 explicitly. Its protocol 2
can corrupt incremental replicas when the source grows beyond a receiver hash
range; SQLite confirmed the [upstream bug and fix](https://sqlite.org/forum/info/46753431d4).
Protocol 1 retains native incremental synchronization, strict version checks,
and validation before publishing each replica.

Remove the protocol override only after upgrading the pinned binary on both
ends and passing the native page-growth regression and appliance recovery tests.
