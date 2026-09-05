package baraccess

import (
	"encoding/binary"
	"fmt"

	"github.com/sercanarga/pcileechgen/internal/pci"
)

const NVMeMSIXReadPolicy = "nvme-msix-dword-v1"

// MSIXSnapshotMode is deliberately separate from the baseline BAR policy.
type MSIXSnapshotMode string

const (
	MSIXSnapshotOff   MSIXSnapshotMode = "off"
	MSIXSnapshotTable MSIXSnapshotMode = "table"
	MSIXSnapshotPBA   MSIXSnapshotMode = "pba"
	MSIXSnapshotAll   MSIXSnapshotMode = "all"
)

func ParseMSIXSnapshotMode(value string) (MSIXSnapshotMode, error) {
	switch MSIXSnapshotMode(value) {
	case "", MSIXSnapshotOff:
		return MSIXSnapshotOff, nil
	case MSIXSnapshotTable, MSIXSnapshotPBA, MSIXSnapshotAll:
		return MSIXSnapshotMode(value), nil
	default:
		return "", fmt.Errorf("--nvme-msix-snapshot must be off, table, pba or all; got %q", value)
	}
}

// MSIXReadRange describes only a capability-declared MSI-X structure. End is
// exclusive. Offset is uint64 to keep all end/bounds arithmetic overflow-safe.
type MSIXReadRange struct {
	Kind     string `json:"kind"`
	BARIndex int    `json:"bar_index"`
	Offset   uint64 `json:"offset"`
	Length   int    `json:"length"`
}

func (r MSIXReadRange) End() uint64 { return r.Offset + uint64(r.Length) }

// PlanNVMeMSIX validates BOTH structures before permitting any access, even
// when only one is selected. Phase 1 supports BAR0 only. It excludes the whole
// controller page and the first doorbell page; higher addresses are permitted
// only when explicitly identified as table/PBA by the PCI capability. This is
// an opt-in policy, not a guarantee against broken hardware/capability metadata.
func PlanNVMeMSIX(bars []pci.BAR, info *pci.MSIXInfo, mode MSIXSnapshotMode) ([]MSIXReadRange, error) {
	parsed, err := ParseMSIXSnapshotMode(string(mode))
	if err != nil || parsed == MSIXSnapshotOff {
		return nil, err
	}
	if info == nil || info.TableSize < 1 || info.TableSize > 2048 {
		return nil, fmt.Errorf("NVMe MSI-X snapshot requires a valid MSI-X capability (1..2048 vectors)")
	}
	if info.TableBIR != 0 || info.PBABIR != 0 {
		return nil, fmt.Errorf("phase-1 NVMe MSI-X snapshot supports table and PBA in BAR0 only")
	}
	var bar0 *pci.BAR
	for i := range bars {
		if bars[i].Index == 0 {
			if bar0 != nil {
				return nil, fmt.Errorf("ambiguous BAR0 metadata")
			}
			bar0 = &bars[i]
		}
	}
	if bar0 == nil || !bar0.IsMemory() || bar0.IsDisabled() {
		return nil, fmt.Errorf("NVMe MSI-X snapshot requires a sized memory BAR0")
	}
	ranges := []MSIXReadRange{
		{Kind: "table", BARIndex: 0, Offset: uint64(info.TableOffset), Length: info.TableSize * 16},
		{Kind: "pba", BARIndex: 0, Offset: uint64(info.PBAOffset), Length: ((info.TableSize + 63) / 64) * 8},
	}
	for _, r := range ranges {
		if r.Offset < 0x2000 || r.Offset%8 != 0 || r.End() > bar0.Size {
			return nil, fmt.Errorf("MSI-X %s range [%#x,%#x) is unaligned, overlaps excluded NVMe pages or exceeds BAR0 size %#x", r.Kind, r.Offset, r.End(), bar0.Size)
		}
	}
	if ranges[0].Offset < ranges[1].End() && ranges[1].Offset < ranges[0].End() {
		return nil, fmt.Errorf("MSI-X table and PBA overlap")
	}
	if parsed == MSIXSnapshotTable {
		return ranges[:1], nil
	}
	if parsed == MSIXSnapshotPBA {
		return ranges[1:], nil
	}
	return ranges, nil
}

// ReadMSIXRange is backend-independent and never reads gaps, widens accesses,
// retries, or returns partial data. read32 receives an absolute BAR offset.
func ReadMSIXRange(r MSIXReadRange, read32 func(uint64) (uint32, error)) ([]byte, error) {
	maxLength := 0
	switch r.Kind {
	case "table":
		maxLength = 2048 * 16
	case "pba":
		maxLength = 256
	}
	if r.BARIndex != 0 || r.Offset < 0x2000 || r.Offset%8 != 0 ||
		r.Length <= 0 || r.Length > maxLength || r.Length%8 != 0 ||
		r.End() < r.Offset || read32 == nil {
		return nil, fmt.Errorf("invalid MSI-X DWORD read range")
	}
	data := make([]byte, r.Length)
	for off := 0; off < r.Length; off += 4 {
		absolute := r.Offset + uint64(off)
		value, err := read32(absolute)
		if err != nil {
			return nil, fmt.Errorf("MSI-X %s BAR%d DWORD at %#x: %w", r.Kind, r.BARIndex, absolute, err)
		}
		binary.LittleEndian.PutUint32(data[off:off+4], value)
	}
	return data, nil
}
