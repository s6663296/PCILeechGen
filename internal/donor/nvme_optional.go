package donor

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"syscall"

	"github.com/sercanarga/pcileechgen/internal/donor/baraccess"
	"github.com/sercanarga/pcileechgen/internal/pci"
)

// collectNVMeOptionalSnapshot reuses baseline CAP/VS before any native-driver
// identity visit. Only diagnostic descriptors are added; BARContents and profiles
// remain unchanged. There is no retry or native-driver/mmap-read fallback.
func (c *Collector) collectNVMeOptionalSnapshot(ctx *DeviceContext) error {
	if !c.options.NVMeOptionalSnapshot {
		return nil
	}
	if !isNVMeClass(ctx.Device.ClassCode) {
		return fmt.Errorf("optional register snapshot requires an NVMe donor")
	}
	slog.Warn("experimental NVMe 1.3 optional descriptor reads enabled; device/platform MMIO faults can still be fatal")
	snapshot, err := c.sysfs.ReadNVMeOptionalSnapshot(ctx.Device.BDF, ctx.BARContents[0], ctx.Device.VendorID, ctx.Device.DeviceID)
	if err != nil {
		return fmt.Errorf("NVMe optional snapshot failed (no retry or blind fallback): %w", err)
	}
	ctx.NVMeOptional = snapshot
	slog.Info("NVMe optional descriptor snapshot complete", "dwords", len(snapshot.Registers),
		"cmb_supported", snapshot.CMB.Supported, "boot_partitions_supported", snapshot.Boot.Supported)
	return nil
}

func (sr *SysfsReader) ReadNVMeOptionalSnapshot(bdf pci.BDF, baseline []byte, vendor, device uint16) (*baraccess.OptionalSnapshot, error) {
	extent, err := baraccess.OptionalExtent(baseline)
	if err != nil {
		return nil, err
	}
	path := filepath.Join(sr.basePath, bdf.String(), "resource0")
	nvme, err := baraccess.ResourceIsNVMe(path)
	if err != nil {
		return nil, err
	}
	if !nvme {
		return nil, fmt.Errorf("PCI class does not identify NVMe")
	}
	driver, err := os.Readlink(filepath.Join(sr.basePath, bdf.String(), "driver"))
	if err != nil || filepath.Base(driver) != "nvme" {
		return nil, fmt.Errorf("optional descriptor reads require the native nvme driver to remain bound")
	}
	cs, err := sr.ReadConfigSpace(bdf)
	if err != nil {
		return nil, err
	}
	if cs.Size < 256 || cs.HeaderLayout() != 0 || !isNVMeClass(cs.ClassCode()) ||
		cs.VendorID() == 0xffff || cs.VendorID() != vendor || cs.DeviceID() != device || cs.Command()&2 == 0 {
		return nil, fmt.Errorf("PCI config identity/class changed, is incomplete or memory-space access is disabled")
	}
	bars, err := sr.ReadResourceFile(bdf)
	if err != nil {
		return nil, err
	}
	validBAR := false
	for _, bar := range bars {
		if bar.Index == 0 && bar.IsMemory() && !bar.IsDisabled() && bar.Size >= uint64(extent) {
			validBAR = true
		}
	}
	if !validBAR {
		return nil, fmt.Errorf("memory BAR0 does not contain the required descriptor extent %#x", extent)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if stat.Size() < int64(extent) {
		return nil, fmt.Errorf("resource0 is too short for descriptor extent %#x", extent)
	}
	mapped, err := syscall.Mmap(int(f.Fd()), 0, extent, syscall.PROT_READ, syscall.MAP_SHARED)
	if err != nil {
		return nil, fmt.Errorf("optional descriptor mmap failed: %w", err)
	}
	defer syscall.Munmap(mapped)
	return baraccess.ReadNVMeOptional(baseline, func(off int) (uint32, error) {
		slog.Info("NVMe optional DWORD read starting", "bar", 0, "offset", fmt.Sprintf("%#x", off))
		return baraccess.Load32(mapped, off)
	})
}
