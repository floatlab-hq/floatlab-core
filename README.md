FloatLab Core
===

FloatLab Core is the brain behind running your FloatLab. It contains several core components that are used
to orchestrate your workloads.

### FloatLab Raft - Distributed Consensus Algorithm

The raft algorithm is used to achieve distributed locking, to ensure that only one application instance
can be running ona single note at one time.

### FloatLab Scheduler - Task Scheduler

This scheduler is used for many core tasks:
- Managing container data replication and backups via ZFS Replication (zfs send/recv)
- Starting and stopping containers on a repeating schedule (like a built-in cron)
- Monitoring the health of containers, restarting if required, and updating scheduler

### FloatLab Control - Management API for Web UI 

This is the main API that is by the Web UI SPA to manage your FloatLab.

### FloatLab Compose  - docker-compose file reader/writer

Contains extended docker-compose file format support, extensions include:

- ZFS Management
  - Snapshot Schedule and Retention Policy
  - Replication Schedules
  - Backup Schedules
  - ZFS Volume Size Limits
- Application Metadata
  - Icon
  - Name
  - HTTP Access URL
- Networking Connection Details

### FloatLab Net - Networking Management

FloatLab needs to manage the host network interface, the ip reservations pool, private internal network, and 
an overlay network

### FloatLab Watch - Monitoring and Alerting

FloatLab Watch is a component that monitors the health of your nodes, containers, and allows for streaming and 
searching container logs.
Alerts need to cover the following scenarios:
- Disk space thresholds
- zfs pool space thresholds
- zpool status - scrub result
- zpool disk faults
- RAM utilization thresholds
- CPU utilization thresholds
- container replication lag
- container backup lag

### FloatLab Auth - Authentication and Authorization

- Issue JWT Tokens via OIDC/OAuth2
- Role-based access control scopes
- User Management

### FloatLab Config 

All the configuration that needs to be stored and distributed to all nodes in the FloatLab 

The data here will be stored using rqlite or dqlite. See more in the [FloatLab Config](./pkg/config/README.md) package.

### FloatLab Connect

FLoatLab connect provides a stable and secure way for all of your FloatLab nodes to communicate with each other, including
operating the raft protocol, replicating the configuration database, and potentially tunneling container traffic between nodes

