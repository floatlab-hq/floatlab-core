# FloatLab stats service

- FloatLab uses a side-car container running VictoriaMetrics to store stats data at a 5s resolution
- The data will be populated using a vmagent/cadvistor/node-exporter
- To view statistics via the web interface, we will query VictoriaMetrics

- Support querying node stats (cpu usage, disk usage, network io, memory usage, disk io)
- Support querying container latency stats (disk i/o latency, http latency, ) including max, avg, p90 and p99
- Support querying container io stats (network up/down, disk read/write)
- Support querying container absolute values (memory usage, disk used, disk free, etc)

We are recording by default statistics 5s interval for 45 days

Need to respond to triggers defined by the floatlab.config, and trigger notifications to floatlab.notify as required, typeical notifications includes:
- node memory usage > 90% 
- zfs filesystem usage > 90%
- zpool usage > 90%
- cpu usage > 90% over 5 average

VictoriaMetrics is running in a sibling container called metrics.
  - host: metrics
  - port: 8428
