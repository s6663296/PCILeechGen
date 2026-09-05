package donor

import (
	"fmt"
	"io"
	"os"

	"github.com/sercanarga/pcileechgen/internal/donor/nvmetrace"
)

// AttachNVMeTrace imports evidence into a saved context. No sysfs or device IO.
func AttachNVMeTrace(ctx *DeviceContext, path, controller string) error {
	if ctx == nil || !isNVMeClass(ctx.Device.ClassCode) {
		return fmt.Errorf("trace import requires an NVMe snapshot")
	}
	if ctx.NVMeTrace != nil {
		return fmt.Errorf("snapshot already contains trace evidence; use the original snapshot to replace it explicitly")
	}
	var size uint64
	for _, bar := range ctx.BARs {
		if bar.Index == 0 && bar.IsMemory() && !bar.IsDisabled() {
			size = bar.Size
		}
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, nvmetrace.MaxBytes+1))
	if err != nil {
		return err
	}
	evidence, err := nvmetrace.Parse(data, ctx.BARContents[0], size, controller, ctx.Device.BDF.String())
	if err != nil {
		return err
	}
	ctx.NVMeTrace = evidence
	return nil
}
