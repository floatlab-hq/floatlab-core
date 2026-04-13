# FloatLab Run Service and State Machine

Each container will have a state machine that will emit lifecycle events via RAFT to all hosts. 
Each running node will keep a local state machine tracking the lifecycle of the container.

## Container State Machine

Initial Step:
- CreateService => Idle
- CreateService => RunningPrimary
- CreateService => RunningBackup

#### Idle - Container is ready to be started
Next States:
- StartedService => RunningPrimary 
- StartedService => RunningBackup
- PurgeService => (Final step)

#### RunningPrimary - Container is up and running on the primary host
Next States:
- FailService => RunningBackup
- StartError/ContainerCrash => Failed
- StopService => Idle

#### RunningBackup - Container is up and running on the backup host
Next States:
- RestoreService => RunningPrimary
- StartError/ContainerCrash => Failed
- StopService => Idle




## Events:


#### CreateStack (stackId, stackConfig)

Create a new stack with the given config.
- CreateFilesystem
- CreateNetworks
- CreateBackupFilesystem


#### CreateService (stackId, containerId, serviceConfig)

Provision the service on the primary host (network, and disk volumes). Create volumes on backup host,
and configure zfs filesystem replication.
- CreateNetwork
- CreatePrimaryVolumes
- CreateBackupVolumes
- CreateZfsReplication
- CreateContainer

#### StartService (stackId, containerId)

Starts container on primary host, and start zfs replication if backup host is configured.
- Configure Network Routing
- StartContainer
- WaitOnHealthCheck

#### FailService (stackId, containerId, mode = auto/manual)

Marks container as failed, due to node failure, or user action
- WaitToConfirmNodeFailure (mode == auto)
- StartContainerOnBackup
- WaitOnHealthCheck

#### RestoreService (stackId, containerId) 

Syncs back the container volumes and restarts service on the primary host.
- PreRestoreBackup - Snapshot volumes on primary host (pre-restore-YYYYMMDD-HHMMSS)
- PreRestoreSync - Sync volumes from the backup host (zfs send / recv from backup to primary)
- PreRestoreStop -  Stop service on the backup host
- RestoreSync - Re-sync volumes from the backup host (zfs send / recv from backup to primary)
- RestoreRun - Start service on the primary host

#### StopService (stackId, containerId)

Stops running container 
- StoppingContainer - Waiting for container to stop 
 
#### PurgeService (stackId, container)

Delete disk volumes, deletes logs, deletes container with networks.
- DeleteContainer
- DeleteNetworks
- DeleteLogs
- DeleteBackupVolumes
- DeletePrimaryVolumes

