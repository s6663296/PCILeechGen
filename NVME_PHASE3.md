# Phase 3: native-driver read-only NVMe snapshot

Phase 3 adds an opt-in diagnostic snapshot through Linux's native `nvme`
driver. It performs no direct BAR access. The driver submits normal NVMe Admin
commands and consequently performs its ordinary queue and doorbell operations.

Reviewed source: NVM Express Revision 1.3, Identify command section 5.15 and
Get Log Page sections 5.14 and 5.14.1:

https://nvmexpress.org/wp-content/uploads/NVM_Express_Revision_1.3.pdf

The implementation is restricted to the donor's reviewed NVMe 1.3.0 Identify
Controller data. It first requests the Active Namespace List and then Identify
Namespace for every returned active NSID. The policy rejects zero, unordered,
duplicate, excessive, or more-than-one-page namespace lists rather than guessing.

The following NVMe 1.3 Get Log Pages are requested with exact specification
lengths. RAE is set so successful reads do not clear corresponding asynchronous
events:

| LID | Name | Bytes | Condition |
| --- | --- | ---: | --- |
| 01h | Error Information | `(ELPE + 1) * 64` | Mandatory |
| 02h | SMART / Health Information | 512 | Mandatory, global NSID |
| 03h | Firmware Slot Information | 512 | Mandatory |
| 04h | Changed Namespace List | 4096 | `OACS.Namespace Management=1` |
| 05h | Commands Supported and Effects | 4096 | `LPA.CELP=1` |
| 06h | Device Self-test | 564 | `OACS.Device Self-test=1` |
| 80h | Reservation Notification | 64 | `ONCS.Reservations=1` |
| 81h | Sanitize Status | 512 | `SANICAP != 0` |

Telemetry 07h/08h is deliberately excluded: host-initiated telemetry can create
a new capture, the total length is device-defined, and a header-only result is
not a complete snapshot. Vendor-specific logs have no specification-derived
safe length or semantics and are also excluded.

No optional command is tried when its Identify capability is clear. There is no
retry, fallback command, write/admin-control opcode, arbitrary LID, or arbitrary
NSID input. Every command is logged before submission. Any ioctl error, nonzero
NVMe status, malformed list, or short result aborts the phase and discards the
partial snapshot. Existing BAR contents, profiles, MSI-X reset data, Identify
firmware model and generated RTL remain unchanged.

The result is stored only in `device_context.json` as `nvme_admin_snapshot`.
It contains raw Identify Namespace and Log Page byte arrays plus policy,
capability, NSID, LID, length and skip evidence. Treat SMART, error and event
logs as time-specific observations rather than reset values.

Enable it with:

```text
--nvme-native-snapshot
```

The donor must already be bound to the native `nvme` driver. The flag is off by
default and rejected with `--from-json`. It may be combined with phase 1 and 2,
but using a fresh output directory preserves the last known-good snapshot.
Immediately before opening the controller node, the backend rechecks native
driver binding, PCI identity/class and Memory Space Enable.

This phase is lower risk than blind MMIO because it uses commands supported by
the active driver, but it cannot guarantee recovery from broken device firmware,
PCIe errors or platform MCEs. Do not repeatedly submit the command after a hang,
AER report or reboot.
