# Management alert evaluator

The management service evaluates enabled management and Compose rules on the
management Raft leader. It starts a pass immediately, then every 30 seconds,
without overlapping passes. Each rule has a three-second metrics-query budget;
a slow pass finishes all rules before the next pass. Database calls use the
existing rqlite client timeouts.

Rules produce persistent incidents per resource. Firing, escalation and recovery
create durable notifications in the same transaction as the incident transition.
Repeated evaluations do not notify again. Resolved incidents remain in `alerts`;
recurrence creates a new incident ID. Reading, resolving or silencing a
notification does not change the measured incident.

## Custom management rules

Create a rule through `POST /api/v1/alert-rules`, for example:

```json
{
  "name": "Low dataset space",
  "condition": "fl_zfs_dataset_available_bytes < 1073741824",
  "recovery_condition": "fl_zfs_dataset_available_bytes > 2147483648",
  "freshness_query": "timestamp(fl_zfs_dataset_available_bytes)",
  "severity": "warning",
  "for_duration": "5m",
  "recovery_duration": "1m",
  "max_sample_age": "2m",
  "annotations": {"description": "Dataset available space has remained below 1 GiB."}
}
```

This example uses the existing dataset metrics, identifies each dataset by its
labels, and is not installed automatically. Calibrate the byte limits for the
intended datasets.

- Firing and recovery expressions are **filtered MetricsQL predicates**. Any
  returned series matches, even when its value is zero. Do not use `bool`
  comparisons, which also return non-matching resources with a zero value.
- All three queries must return identical, stable resource labels. Aggregate away
  container IDs and other transient labels. The example retains the dataset
  metric's labels; derived queries must aggregate
  all three expressions to the same resource labels.
- `freshness_query` returns Unix timestamps from the required source observations,
  using the oldest timestamp where multiple sources are needed. The timestamp of
  a derived calculation is not proof of fresh collection. Authors must ensure all
  required sources exist in this query; an `or` alone cannot establish that both
  operands exist.
- Empty, stale, invalid, conflicting or failed observations are unknown. They do
  not resolve incidents, and reset pending/recovery continuity. Re-reading one
  source timestamp cannot advance a timer.
- Durations use Go syntax (`30s`, `5m`, `2h`). Firing may be immediate (`0s` or
  empty); recovery defaults to `1m`, and maximum sample age defaults to `2m`.
- Required recovery/freshness fields are validated before creating or enabling a
  rule. MetricsQL syntax is validated by the backend during evaluation; errors are
  visible in the rule's `evaluations` response.

Legacy rules are preserved. Rules missing recovery/freshness expressions remain
unknown until updated, and can still be disabled/deleted. Material condition edits
reset timers; disabling/deleting closes incidents with an explicit reason.

## Compose integration

`x-fl-alert-rules` uses its existing numeric comparator contract. The adapter
generates complementary firing/recovery expressions and source timestamp queries.
CPU, memory percentage, lifetime restart count and managed-volume space are
supported. Service/replica identity survives container replacement and active-node
changes; the incident retains the current node as metadata. Managed mount selectors
match mount source, target or dataset, with an empty selector applying to all mounts.

Docker telemetry now includes service/replica labels, enforced memory limit and
Docker's lifetime restart count. An unlimited container has no memory percentage.
The lifetime restart count resets on replacement; persistent event windows are
still needed for the catalogue's cross-replacement restart-loop defaults.

## Persistence and deployment

`alert_evaluations` stores timers, source timestamps, status and incident identity.
Conditional versioned writes prevent competing evaluations from duplicating a
transition. Restart/owner changes reuse incidents but count elapsed time toward
a duration only while observation continuity remains valid. Notifications are
durable; SSE is a best-effort hint and consumers can fetch notification history.

The default Compose deployment no longer starts vmalert. The existing webhook
remains available for separately owned external rules; do not evaluate the same
rule in both systems. Its legacy identity/history semantics are unchanged.

This change installs the evaluator and existing-rule adapters, not the proposed
built-in catalogue or new host/SMART/ZFS/protection collectors. Setup alerts,
notification delivery endpoints, incident maintenance/inhibition policies and
automatic severity bands remain separate work. A rule's configured severity can
be changed without replacing its active incident.

See [the rule catalogue](../../internal/control/ALERT_RULES.md) for the broader
requirements and proposed defaults.

## Appliance integration test

Against a dedicated test appliance reachable through the local QEMU guest agent:

```sh
FLOATLAB_VM_INTEGRATION=1 API_URL=http://VM_IP:8080 VM_NAME=TEST_VM \
  LIBVIRT_URI=qemu:///system go test -count=1 -timeout 15m \
  -run '^TestAlertEvaluator$' -v ./integration
```

For an SSH-accessible test VM, set `VM_SSH=user@VM_IP` instead of `VM_NAME`.
The test pushes scoped samples to real VictoriaMetrics, restarts management and
checks independent incidents, deduplication, unknown/stale data, recovery,
recurrence and rule disablement. It cleans up its own rule and records. The full
`scripts/test-workloads.sh` workflow includes this scenario too.
