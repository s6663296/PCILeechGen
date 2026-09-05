# Phase 2: NVMe 1.3 optional descriptor snapshot

This phase is opt-in and diagnostic-only. It does not restore a full BAR sweep,
add active probing, clone controller memory or change firmware initialization.

## Reviewed source and scope

NVM Express Revision 1.3, sections 3 and 3.1.1, 3.1.11 through 3.1.15,
printed pages 36-37 and 44-46:

https://nvmexpress.org/wp-content/uploads/NVM_Express_Revision_1.3.pdf

The source was downloaded and its register tables inspected, including visual
inspection of page 44. Section 3 permits native-width or aligned DWORD accesses,
forbids locked accesses, and says cross-register accesses are unsupported.
This patch reuses the existing x86 aligned DWORD load accessor (no locked RMW).

| Register | Access condition | Behavior |
| --- | --- | --- |
| CMBSZ, 0x3c | Reviewed VS=0x00010300, valid baseline CAP, enabled/ready controller | Read first. The specification requires zero when CMB is unsupported. |
| CMBLOC, 0x38 | CMBSZ nonzero with valid NVMe 1.3 fields | Read second; skip when CMBSZ=0 because CMBLOC is then reserved. |
| BPINFO, 0x40 | CAP.BPS (bit 45) is set | Read boot-partition status/size only. Skip when unsupported. |
| NSSR, 0x20 | None | Reset control remains excluded even if NSSRS=1. |
| BPRSEL/BPMBL, 0x44..0x4f | None | Boot-transfer control/address registers remain excluded in this descriptor-only phase. |
| Later-version registers, CMB contents, vendor regions, reserved space, doorbells | None | Not accessed. |

The exact review applies to VS=1.3.0; 1.2, 1.4 and 2.x are rejected instead of
assuming identical semantics. Later review can expand version support without
turning this into a byte-range scan. Invalid/reserved descriptor encodings and
all-ones responses stop the phase, discarding partial output. No automatic
retry or bulk-read fallback exists. The read callback never follows a CMB BIR
or address; reported CMB size is metadata, not verified usable capacity. The
specification permits capacity to be limited by the actual target BAR extent.

## Supplied donor evidence

The user's successful phase-1 snapshot reports CAP=0x0010003078013fff and
VS=0x00010300. CAP.BPS=0, so BPINFO will be skipped; NSSRS=1 does not enable
NSSR access. CMBSZ was not in the previous snapshot, so CMB support is genuinely
unknown until a new read is successfully obtained. A zero CMBSZ is a useful
negative capability observation, not a failed attempt to collect CMBLOC.

Thus this donor gets one new DWORD read (CMBSZ), or two if valid CMBSZ reports
CMB support (CMBSZ then CMBLOC). No command causes an automatic progression into
more addresses based on whether the host survived.

## Integration and evidence handling

- `baraccess.OptionalExtent` validates the prior snapshot and computes the
  maximum descriptor mapping extent (0x40 or 0x44).
- `baraccess.ReadNVMeOptional` performs the conditional reads, decoding and
  explicit skipped-field accounting. Tests inject an access recorder.
- `SysfsReader.ReadNVMeOptionalSnapshot` requires native `nvme` binding, valid
  fresh PCI config identity/class, MSE, memory BAR0 size and resource-file bounds.
  It opens a read-only compact mapping, logs each attempted DWORD and issues
  only policy-selected loads. OS page-rounding does not cause a bulk read.
- `Collector.Collect` invokes the phase immediately after baseline collection,
  before the identity native-driver visit; profiling still uses the RAM snapshot.
  The option requires `nvme` already bound, refusing a VFIO-bound donor rather
  than automatically rebinding it for this phase. Existing baseline behavior
  outside this opt-in remains unchanged.
- Results are in `device_context.json` under `nvme_optional_snapshot`, carrying
  the policy identifier, baseline version/CAP, observed registers, decoded CMB/
  boot descriptors and explicit skip reasons. The enclosing context supplies
  the collection timestamp. Only `registers` entries are measured values;
  unsupported descriptors and skip entries are not fabricated zero reads.
- The new object is preserved by JSON roundtrip, but not copied into
  `bar_contents`, `bar_profiles`, Identify, MSI-X reset entries or generated RTL.
  Offline `--from-json` builds retain it without accessing hardware.

This is a sequential diagnostic snapshot, not a transactionally consistent
controller image. Binding/state can change concurrently; preflight checks do
not make a failing endpoint safe. There is no MCE recovery guarantee. Stop on
hardware errors or reboot rather than repeating the experiment.

## Usage after deploying this source revision

Default: no extra reads. Build the updated source first. For an explicitly
chosen donor maintenance check, the new flag is:

```text
--nvme-optional-snapshot
```

It may be combined with the existing `--nvme-msix-snapshot=all`; neither flag
means full BAR scanning. Prefer a new output directory to preserve the known
working phase-1 snapshot. Do not run on an in-use/mounted donor. The option is
rejected with `--from-json`; omit it for offline builds.

Expected completion log:

```text
NVMe optional descriptor snapshot complete dwords=1 cmb_supported=false boot_partitions_supported=false
```

That line is an example for an unsupported CMB, not a prediction of this
controller's CMBSZ value. If CMB is reported, the count is two for this donor.
No phase-2 hardware result has been obtained during implementation.

## Verification

Policy tests cover conditional access order, no reads of unsupported CMBLOC or
BPINFO, malformed version/state rejection before access, all-ones/error stopping,
reserved encodings and large descriptor arithmetic without following pointers.
Backend/collector tests use fake sysfs resources and test default-off behavior,
read-only file integrity, preflight failures, option wiring, JSON roundtrip and
baseline data isolation. CLI tests reject live flags in offline mode.

Windows-host execution: `go test ./internal/donor/baraccess`.
Linux backend/CLI tests require Linux; cross-compilation is not execution:

```sh
go test ./internal/donor/... ./cmd/pcileechgen ./internal/firmware/output
```

At publication, policy tests executed successfully on the Windows editing host;
the Linux/amd64 full build, relevant test-binary compilation and `go vet ./...`
also passed. Linux backend tests were not executed on that host, and physical
hardware validation remains pending. This is a reviewed descriptor subset, not
completion of every optional controller function.
