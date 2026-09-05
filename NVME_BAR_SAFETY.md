# NVMe live BAR collection safety

For the opt-in NVMe 1.3 optional-descriptor phase, its specification references,
capability gates and exclusions, see [NVME_PHASE2.md](NVME_PHASE2.md).

## Finding

The former sysfs collection path was `Collector.Collect` -> `collectBARMemory`
-> `readBARs` -> `SysfsReader.ReadBARContent` -> `readBARViaMmap`. It copied up to
64 KiB directly from the BAR mapping using Go `copy`/memmove. The 64 KiB cap was
a size/performance limit, not a register-safety policy.

`collectBARProfiles` then reopened each eligible BAR through `BARProfiler.ProfileBAR`
and `snapshotRegisters`, reading every DWORD again. Missing initial snapshots
did not prevent that second scan. `captureViaNativeDriver` -> `readBARUntilValid`
also used the broad sysfs read; the standalone `vfio.Collect` had its own broad
pread/mmap route (and did not actually apply its declared 64 KiB cap).

For NVMe, doorbells start at BAR0 offset 0x1000; their stride is derived from
CAP.DSTRD. They are command/queue notification registers, not a readable register
image to bulk-dump. Reserved, optional, vendor-specific and controller-memory
regions likewise must not be treated as ordinary RAM. Even below 0x1000, a blind
scan is not equivalent to reading specified baseline registers. A read-only
mapping protects against software writes, not fatal PCIe read completions.

This identifies a concrete unsafe access pattern consistent with the reported
deep-read -> AER -> MCE/Data Fabric sync-flood reboot. It does **not** prove which
transaction failed on the Intel 8086:f1a6 device without hardware/error logs.
IOMMU isolation constrains DMA; it does not make CPU MMIO reads safe. Link-speed
and power/overclock changes cannot fix an invalid access pattern in software.

## Implemented policy

The policy is selected by PCI class 0x010802, not vendor/device ID. It therefore
applies to this Intel controller and other NVMe controllers.

| BAR0 offsets | Read contents |
| --- | --- |
| 0x00, 0x04 | CAP, as two DWORDs |
| 0x08 | VS |
| 0x0c, 0x10 | INTMS, INTMC (live values, not guaranteed meaningful with MSI-X) |
| 0x14, 0x1c | CC, CSTS |
| 0x24 | AQA |
| 0x28, 0x2c, 0x30, 0x34 | ASQ, ACQ, as DWORD pairs |

By default, only these twelve aligned 32-bit reads are issued. Reserved 0x18, optional
subsystem reset 0x20 (NSSR), all offsets >= 0x38, and other NVMe BARs are skipped.
In particular, no doorbell at/after 0x1000 is read or written. The opt-in MSI-X
phase described below reads only capability-declared table/PBA structures.

- `internal/donor/baraccess/nvme.go`: shared `ReadNVMe`, `NVMeOffsets`,
  `ResourceIsNVMe` and `Load32`. Missing/malformed class metadata fails closed.
  All-ones CAP stops that snapshot after two reads; read errors discard partial
  output instead of allowing padding to disguise an inaccessible device.
- `internal/donor/sysfs.go`: `ReadBARContent` dispatches to
  `readNVMeBARViaMmap` for NVMe. The latter uses a read-only compact mapping and
  exact DWORD loads, with no fallback to a broad read. The OS may page-round the
  mapping, but the code dereferences only the allowlisted offsets. The mmap
  accessor is restricted to reviewed x86/amd64 load semantics; other architectures
  fail closed. VFIO's DWORD pread path does not use that architecture restriction.
- `internal/donor/bar_profiler.go`: direct `ProfileBAR` also enforces the policy
  and refuses active NVMe probing. `snapshotBARProfile` lists only observed
  DWORDs. The buffer-only active-probe helper remains for ordinary RAM tests.
- `internal/donor/collector.go`: `collectBARProfiles` consumes captured RAM,
  never reopens hardware, and skips missing/all-ones snapshots. This reuse also
  removes the duplicate live read for non-NVMe devices. Initial reads and native
  driver retries inherit the policy through `ReadBARContent`.
- `internal/donor/vfio/reader.go`: `Collect` detects class from validated config
  data and calls `readNVMeRegion`, issuing one four-byte pread per allowed offset
  with no broad fallback. Non-NVMe VFIO collection now honors the existing 64 KiB cap.
- `internal/firmware/barmodel/model.go`: `BuildBARModel` selects the existing
  NVMe spec model for policy-tagged snapshots, retaining required write masks
  and currently-zero registers rather than treating zero probe masks as read-only.
  `barprofile/profile.go` reports unknown access behavior and a spec-model hint;
  `devicemodel/builder.go` uses spec masks with inferred (not measured) confidence.

No blind-scan override was added. Non-NVMe live reads otherwise retain their existing
behavior; this is not a universal safe-register policy for arbitrary PCI devices.

## Completeness and tradeoffs

This is a **restricted baseline snapshot, not a complete device clone**.

- PCI configuration/capability collection and original BAR aperture metadata are
  unchanged. The snapshot length does not replace the PCI BAR size.
- Existing sysfs identity and admin Identify collection are unchanged: controller
  model, serial and firmware, plus raw 4096-byte Controller and Namespace Identify
  pages when ioctl succeeds. The existing namespace path targets NSID 1; it does
  not enumerate all namespaces. The native-driver visit/reset/rebind behavior is
  unchanged and can independently fail or disrupt a device.
- BAR0 output is 56 bytes (0x38), containing 48 observed bytes and eight zero
  placeholders at 0x18 and 0x20. These placeholders are **not measurements**.
  `bar_profiles["0"].read_policy = "nvme-baseline-dword-v1"` documents the policy;
  its probes list only the twelve observed DWORDs. Old readers can ignore the
  optional JSON field. No large fabricated full-BAR dump is generated.
- RW/W1C behavior is not measured. Zero probe masks must not be read as proof
  that a hardware register is read-only. Existing spec-based emulation remains
  responsible for controller behavior. The snapshot is sequential, not atomic
  across registers, and live queue addresses/status are not power-on defaults.
- Optional features (e.g. CMB/PMR registers), vendor regions, doorbells, other BAR
  contents and, by default, live MSI-X table/PBA contents are not captured. MSI-X capability
  layout/vector count remains available from config space. The existing output
  path initializes absent MSI-X entries as masked, to be programmed by the host.
- These omissions preserve inputs for baseline NVMe modeling but cannot guarantee
  compatibility or faithful behavior for every controller feature. Additional
  registers should be added only after checking the controller version, capability
  bits, access width and register semantics, with corresponding access tests.

The patch reduces the identified risk; it cannot guarantee that even baseline
reads are safe on a failed endpoint/platform. A machine-check reset cannot be
reliably caught by Go error handling, signal recovery, a timeout or a try/catch.
No real PCI MMIO, driver rebinding or hardware repro was performed during this
patch. Do not restore deep reads merely to confirm the original failure. A
non-boot donor may still contain mounted filesystems or active workloads.

## Verification

On the Windows editing host with Go 1.26.2:

- `go test ./internal/donor/baraccess` passed (actual test execution).
- `GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build ./...` passed.
- Linux test binaries for donor packages and the affected firmware consumers
  compiled successfully; these Linux tests were **not executed** on Windows.
- Linux-targeted `go vet ./internal/donor/...` passed.

Tests cover the exact offset list, rejection before access, error short-circuiting,
all-ones CAP, missing/invalid class data, sysfs and native-retry routing, read-only
profiling, snapshot reuse, compact-buffer holes, VFIO DWORD short reads and
preservation of spec write masks, BAR aperture and honest model confidence.

On a Linux development machine, run the following against mock files only
(no sudo or donor hardware is required):

```sh
go test ./internal/donor/... ./internal/firmware/barmodel ./internal/firmware/barprofile ./internal/firmware/output ./internal/firmware/devicemodel
```

## Phase 1: opt-in, exact MSI-X Table / PBA snapshots

`build --nvme-msix-snapshot=off|table|pba|all` adds one explicitly selected
diagnostic phase. The default is `off`, preserving the previously working
56-byte NVMe BAR0 collection. `table` and `pba` can be tried independently;
`all` reads table first, then PBA. This is **not full BAR scanning**. The option
requires live `--bdf` collection on an NVMe donor and is rejected with
`--from-json`. Offline builds can still consume saved diagnostic snapshots
without that option and without accessing hardware.

For the reported Intel controller's capability layout (16 vectors, both in
BAR0), the extra reads are:

| Mode | Exact byte range (inclusive) | Extra DWORD reads |
| --- | --- | --- |
| `table` | 0x2000..0x20ff | 64 |
| `pba` | 0x2100..0x2107 | 2 |
| `all` | Both of the above | 66 |

These offsets are examples from the device log, **not hardcoded addresses**.
`PlanNVMeMSIX` derives lengths as `vectors * 16` and `ceil(vectors / 64) * 8`.
Both structures must be in BAR0, eight-byte aligned, disjoint, at/above 0x2000
and within the reported memory BAR. Phase 1 deliberately does not support
other BIRs. Metadata for both structures is validated even when only one is
selected. Valid MSI-X capability geometry is relied upon to identify the
structures; it cannot prove that a faulty device will complete a read safely.

`ReadNVMeMSIXSnapshot` freshly reads PCI class/config/resource metadata,
requires memory-space enable, and rejects missing/changed MSI-X geometry.
All selected ranges must also fit the actual resource0 file size before any
range is read. No guessed aperture is used if resource metadata is unavailable.
Mappings start at the relevant page; only the requested DWORD offsets are
dereferenced. Gaps and page-rounded padding are never copied or swept. PBA is
read as two DWORDs per 64-bit block, so live changes can yield a non-atomic
snapshot. No mask, interrupt, queue or driver state is changed by this phase.

The phase runs once after baseline collection, NVMe identity capture and BAR
validation. It is not added to the native-driver retry loop, direct profiler or
standalone VFIO collector. There is no active write probing, retry, larger-range
fallback or raw read fallback. A software-detected failure returns a build error
and does not publish a partial-success snapshot. Range start/end and every
DWORD's attempted offset are logged before access. A hard reboot can still lose
logs and cannot be recovered by the application; a last logged address is a
diagnostic lead, not proof of the exact failed PCIe transaction.

Completed data is saved under:

```text
device_context.json
  msix_data.snapshot
    read_policy: "nvme-msix-dword-v1"
    mode: "table" | "pba" | "all"
    collected_at: UTC timestamp
    ranges[]: kind, bar_index, offset, length, data (base64)
```

These bytes are kept separate from `bar_contents`, `bar_profiles` and
`msix_data.entries`. In particular, live interrupt addresses and pending bits
are not used to initialize the FPGA. Existing masked, zero-address MSI-X reset
initialization remains unchanged. Snapshot data may contain host interrupt
routing addresses; review it before sharing publicly.

### Suggested first hardware check

Keep the old working output directory. Build the updated source (`make build`)
and start with the table-only mode in a **new** output directory:

```sh
set -o pipefail
sudo ./bin/pcileechgen build \
  --bdf 0000:01:00.0 \
  --board CaptainDMA_75T \
  --skip-vivado \
  --nvme-msix-snapshot=table \
  --output ./my_nvme_msix_table \
  2>&1 | tee nvme-msix-table.log
```

Do not run on a mounted/in-use donor, and preserve any needed donor data first.
Check kernel AER/MCE messages as well as command completion. Only after a clean
table run should PBA be tried separately, using `--nvme-msix-snapshot=pba` and
another output directory/log. Do not automate progression or retry after a
machine check. `off` restores the baseline policy; `all` is available once both
individual regions have been assessed. Passing this phase still does not justify
reading doorbells, reserved space or unspecified optional registers.

Phase-1 tests add exact per-mode access traces, vector rounding, overflow and
overlap rejection, gap avoidance, page-relative mapping, config/resource
preflight, read-error short-circuiting, JSON roundtrip and a firmware-output
regression that rejects copying diagnostic host MSI-X data into initialization.
On the Windows editing host, policy tests are executable; Linux backend/CLI and
output tests are cross-compiled, not hardware-tested. Run the additional CLI
tests on Linux with `go test ./cmd/pcileechgen`.
