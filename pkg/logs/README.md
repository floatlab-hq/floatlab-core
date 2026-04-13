FloatLab Logging Service
===

- FloatLab will run an instance of VictoriaLogs in a sidecar container.
- VictoriaLogs will be configured to write logs to a local volume
- FloatLab will provide a REST API for searching logs by service with a date-range.
- FloatLab Logs will provide a configuration to write logs to all hosts in the cluster.
- When a container stack is deleted, and then purged, the logs should be removed at the same time.

We will run VictoriaLogs for all container logs, system logs, and host daemon logs.
Journalctl will be configured to keep only a few hours of logs.