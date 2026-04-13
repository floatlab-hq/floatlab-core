# Floatlab Notification Service (floatlab.notify)

This service is responsive for communicating to the user that there is something requiring thier attention.
The notifications come in the following flavours:
- persistent - requires user attention until resolved (eg high disk usage, high ram usage, faulty disk, etc)
  - silence - temporarily suppress notification for a period of time
- one-shot - informing the user of an event of interest (container restarted, host restored, ...)



Notifications should be stored on all nodes, and should be persisted to disk for future reference. Once a notification
is resolved, it should be kept for 30 days, then cleaned up.

