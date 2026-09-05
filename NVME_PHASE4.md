# Phase 4A: offline trace evidence import

Build with the saved phase-3 context and text trace:

```sh
./bin/pcileechgen build \
  --from-json ./my_nvme_phase3/device_context.json \
  --nvme-trace ./nvme-phase4a-trace.log \
  --nvme-trace-controller nvme0 \
  --board CaptainDMA_75T --skip-vivado --output ./my_nvme_phase4
```

No root privileges or donor access is needed. Both trace flags require
`--from-json`. The controller name is an explicit user assertion that the
trace belongs to the saved donor: the trace itself contains no BDF, serial,
kernel version or boot identity. Preserve the original trace and context.
An existing evidence object is preserved on normal offline builds; attaching
another trace to it is rejected. Use the original context to select a new trace.

`device_context.json` and `device_model.json` contain `nvme_trace_evidence`:
source SHA-256, controller association, capture counters, commands paired by
QID/CID, completion statuses/retries, timestamps, and separate SQ head/tail
samples. Unmatched completions and unfinished commands are explicitly counted.
Failed commands are retained, not classified as successful captures.

The parser accepts the reviewed ftrace text format with six-decimal timestamps.
Files above 8 MiB, missing/inconsistent buffer counters, malformed event lines,
numeric overflow, duplicate in-flight QID/CID, reversed paired timestamps and
doorbell offsets outside saved BAR0 are rejected. Other-controller events are
counted and excluded. Matching buffer counts do not prove absence of all loss.

For each observed QID, specified offsets are derived from saved CAP.DSTRD:
stride = 4 << DSTRD; SQ = 0x1000 + 2 * QID * stride; CQ = SQ + stride.
This describes register locations only. SQ samples do not measure CQ heads or
doorbell writes. `mmio_observed` is false. Unobserved I/O queues, reset values,
queue depths, DBBUF state and hardware latency are not inferred. In particular,
a software setup-to-completion interval includes scheduling and driver overhead.
The evidence field does not feed RTL generation, reset images or latency seeds.

The supplied phase-4A capture contains only QID 0; it does not establish I/O queue
behavior or full device emulation. Stages 1-4 improve documented evidence but do
not make an exhaustive BAR dump, protocol model or hardware validation complete.

For future capture tooling use a dedicated tracefs instance, controller filters,
and restore prior tracing state. The earlier manual global-trace recipe cleared
the global buffer and did not restore prior settings. This importer does not
enable tracing, rebind a device, or enable mmiotrace.

Validation: synthetic parser tests and local replay of the private supplied
trace run on Windows. Linux backend/CLI tests are cross-compiled, not executed
on the Windows editing host. Hardware and Vivado validation remain separate.
