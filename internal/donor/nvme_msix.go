package donor

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/sercanarga/pcileechgen/internal/donor/baraccess"
	"github.com/sercanarga/pcileechgen/internal/pci"
)

// MSIXSnapshot is diagnostic live state, never a firmware reset image. Data
// uses the standard JSON base64 encoding for []byte and stores no unread gaps.
type MSIXSnapshot struct {
	ReadPolicy  string              `json:"read_policy"`
	Mode        string              `json:"mode"`
	CollectedAt time.Time           `json:"collected_at"`
	Ranges      []MSIXRangeSnapshot `json:"ranges"`
}

type MSIXRangeSnapshot struct {
	baraccess.MSIXReadRange
	Data []byte `json:"data"`
}

// collectNVMeMSIXSnapshot runs once, after baseline/identity collection and
// validation. It never participates in native-driver fallback or BAR profiling.
func (c *Collector) collectNVMeMSIXSnapshot(ctx *DeviceContext, mode baraccess.MSIXSnapshotMode) error {
	if mode == baraccess.MSIXSnapshotOff {
		return nil
	}
	if !isNVMeClass(ctx.Device.ClassCode) || ctx.MSIXData == nil {
		return fmt.Errorf("NVMe MSI-X snapshot requested but NVMe/MSI-X metadata is unavailable")
	}
	slog.Warn("experimental NVMe MSI-X snapshot enabled; even targeted MMIO can cause a fatal PCIe/MCE reboot", "mode", mode)
	snapshot, err := c.sysfs.ReadNVMeMSIXSnapshot(ctx.Device.BDF, pci.ParseMSIXCap(ctx.ConfigSpace), mode)
	if err != nil {
		return fmt.Errorf("requested NVMe MSI-X snapshot failed (no retry or blind fallback): %w", err)
	}
	ctx.MSIXData.Snapshot = snapshot
	// Do NOT populate Entries or BARContents. Existing code can use those for
	// firmware initialization; these host-programmed addresses are diagnostic.
	return nil
}

// ReadNVMeMSIXSnapshot re-reads config/class/resource metadata, rejecting changed
// capability geometry before opening MMIO. It accepts no user-supplied offsets.
func (sr *SysfsReader) ReadNVMeMSIXSnapshot(bdf pci.BDF, expected *pci.MSIXInfo, mode baraccess.MSIXSnapshotMode) (*MSIXSnapshot, error) {
	mode, err := baraccess.ParseMSIXSnapshotMode(string(mode))
	if err != nil || mode == baraccess.MSIXSnapshotOff {
		return nil, err
	}
	resourcePath := filepath.Join(sr.basePath, bdf.String(), "resource0")
	nvme, err := baraccess.ResourceIsNVMe(resourcePath)
	if err != nil {
		return nil, err
	}
	if !nvme {
		return nil, fmt.Errorf("MSI-X extended snapshot is only supported for NVMe")
	}
	cs, err := sr.ReadConfigSpace(bdf)
	if err != nil {
		return nil, err
	}
	if cs.Size < pci.ConfigSpaceLegacySize || cs.VendorID() == 0xffff ||
		!isNVMeClass(cs.ClassCode()) || cs.HeaderLayout() != 0 {
		return nil, fmt.Errorf("NVMe config space is incomplete, unreachable or has unexpected class/header")
	}
	if cs.Command()&0x02 == 0 {
		return nil, fmt.Errorf("PCI memory space is disabled; refusing experimental MSI-X reads")
	}
	info := pci.ParseMSIXCap(cs)
	if !sameMSIXLayout(expected, info) {
		return nil, fmt.Errorf("MSI-X capability is missing or layout changed since baseline collection")
	}
	bars, err := sr.ReadResourceFile(bdf)
	if err != nil {
		return nil, err // no guessed BAR-size fallback for experimental reads
	}
	plan, err := baraccess.PlanNVMeMSIX(bars, info, mode)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(resourcePath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil {
		return nil, err
	}
	// Preflight every selected range before reading any DWORD.
	for _, r := range plan {
		if stat.Size() <= 0 || r.End() > uint64(stat.Size()) {
			return nil, fmt.Errorf("MSI-X %s [%#x,%#x) exceeds resource0 file size %d", r.Kind, r.Offset, r.End(), stat.Size())
		}
	}
	snapshot := &MSIXSnapshot{ReadPolicy: baraccess.NVMeMSIXReadPolicy, Mode: string(mode), CollectedAt: time.Now().UTC()}
	for _, r := range plan {
		slog.Info("NVMe MSI-X range read starting", "kind", r.Kind, "bar", r.BARIndex,
			"offset", fmt.Sprintf("%#x", r.Offset), "end_exclusive", fmt.Sprintf("%#x", r.End()), "bytes", r.Length)
		data, err := readMSIXRangeViaMmap(f, r)
		if err != nil {
			return nil, err // no partial-success artifact, retries or read() fallback
		}
		snapshot.Ranges = append(snapshot.Ranges, MSIXRangeSnapshot{MSIXReadRange: r, Data: data})
		slog.Info("NVMe MSI-X range read complete", "kind", r.Kind, "bytes", len(data), "dwords", len(data)/4)
	}
	return snapshot, nil
}

func sameMSIXLayout(a, b *pci.MSIXInfo) bool {
	return a != nil && b != nil && a.TableSize == b.TableSize &&
		a.TableBIR == b.TableBIR && a.TableOffset == b.TableOffset &&
		a.PBABIR == b.PBABIR && a.PBAOffset == b.PBAOffset
}

func readMSIXRangeViaMmap(f *os.File, r baraccess.MSIXReadRange) ([]byte, error) {
	pageSize := uint64(os.Getpagesize())
	pageBase := r.Offset / pageSize * pageSize
	delta := r.Offset - pageBase
	// Length is <= 32 KiB; no mapping or allocation proportional to BAR offset.
	mapped, err := syscall.Mmap(int(f.Fd()), int64(pageBase), int(delta)+r.Length,
		syscall.PROT_READ, syscall.MAP_SHARED)
	if err != nil {
		return nil, fmt.Errorf("MSI-X %s mmap [%#x,%#x): %w", r.Kind, r.Offset, r.End(), err)
	}
	defer syscall.Munmap(mapped)
	return baraccess.ReadMSIXRange(r, func(off uint64) (uint32, error) {
		// Deliberately visible without a debug flag: the last attempted address
		// is valuable when a fatal hardware error prevents a normal Go error.
		slog.Info("NVMe MSI-X DWORD read starting", "kind", r.Kind, "bar", r.BARIndex, "offset", fmt.Sprintf("%#x", off))
		return baraccess.Load32(mapped, int(off-pageBase))
	})
}
