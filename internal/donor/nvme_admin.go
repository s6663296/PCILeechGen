package donor

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"unsafe"

	"github.com/sercanarga/pcileechgen/internal/donor/nvmeadmin"
	"github.com/sercanarga/pcileechgen/internal/pci"
)

func (sr *SysfsReader) ReadNVMeAdminSnapshot(bdf pci.BDF, rawController []byte, vendor, device uint16) (*nvmeadmin.Snapshot, error) {
	driver, err := os.Readlink(filepath.Join(sr.basePath, bdf.String(), "driver"))
	if err != nil || filepath.Base(driver) != "nvme" {
		return nil, fmt.Errorf("native snapshot requires the nvme driver to remain bound")
	}
	cs, err := sr.ReadConfigSpace(bdf)
	if err != nil {
		return nil, err
	}
	if cs.Size < 256 || cs.HeaderLayout() != 0 || !isNVMeClass(cs.ClassCode()) || cs.VendorID() != vendor || cs.DeviceID() != device || cs.Command()&2 == 0 {
		return nil, fmt.Errorf("PCI config identity/class changed, is incomplete or memory-space access is disabled")
	}
	nvmeDir := filepath.Join(sr.basePath, bdf.String(), "nvme")
	entries, err := os.ReadDir(nvmeDir)
	if err != nil {
		return nil, fmt.Errorf("native nvme driver not bound: %w", err)
	}
	ctrlName := ""
	for _, entry := range entries {
		if entry.IsDir() && strings.HasPrefix(entry.Name(), "nvme") {
			ctrlName = entry.Name()
			break
		}
	}
	if ctrlName == "" {
		return nil, fmt.Errorf("no nvme controller under %s", nvmeDir)
	}
	devPath := "/dev/" + ctrlName
	f, err := os.OpenFile(devPath, os.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", devPath, err)
	}
	defer f.Close()
	return nvmeadmin.Capture(rawController, func(req nvmeadmin.Request) ([]byte, error) {
		buf := make([]byte, req.Length)
		cdw10, cdw11, cmdErr := nvmeadmin.CommandDwords(req)
		if cmdErr != nil {
			return nil, cmdErr
		}
		cmd := nvmeAdminCmd{opcode: req.Opcode, nsid: req.NSID, addr: uint64(uintptr(unsafe.Pointer(&buf[0]))), dataLen: uint32(len(buf)), cdw10: cdw10, cdw11: cdw11, timeoutMs: 5000}
		slog.Info("NVMe native read-only admin command starting", "name", req.Name, "opcode", fmt.Sprintf("%#x", req.Opcode), "nsid", fmt.Sprintf("%#x", req.NSID), "lid", fmt.Sprintf("%#x", req.LID), "bytes", req.Length, "rae", req.RAE)
		ret, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), nvmeIOCAdminCmd, uintptr(unsafe.Pointer(&cmd)))
		runtime.KeepAlive(buf)
		if errno != 0 {
			return nil, errno
		}
		if ret != 0 {
			return nil, fmt.Errorf("NVMe status %#x", ret)
		}
		return buf, nil
	})
}

func (c *Collector) collectNVMeAdminSnapshot(ctx *DeviceContext) error {
	if !c.options.NVMeAdminSnapshot {
		return nil
	}
	if ctx.NVMeIdentity == nil || len(ctx.NVMeIdentity.RawControllerIdent) != 4096 {
		return fmt.Errorf("native read-only snapshot requires successful raw Identify Controller capture")
	}
	slog.Warn("experimental NVMe native read-only admin snapshot enabled; commands use the bound nvme driver and may still expose controller/platform faults")
	snapshot, err := c.sysfs.ReadNVMeAdminSnapshot(ctx.Device.BDF, ctx.NVMeIdentity.RawControllerIdent, ctx.Device.VendorID, ctx.Device.DeviceID)
	if err != nil {
		return fmt.Errorf("NVMe native read-only snapshot failed (no retry): %w", err)
	}
	ctx.NVMeAdmin = snapshot
	slog.Info("NVMe native read-only snapshot complete", "namespaces", len(snapshot.Namespaces), "log_pages", len(snapshot.LogPages))
	return nil
}
