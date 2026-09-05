package donor

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/sercanarga/pcileechgen/internal/donor/vfio"
	"github.com/sercanarga/pcileechgen/internal/pci"
)

func nvmeOptionalFixture(t *testing.T, cmb bool) (*Collector, *DeviceContext, string, []byte) {
	t.Helper()
	c, ctx, dir, data := nvmeMSIXFixture(t)
	driverDir := filepath.Join(c.sysfs.basePath, "drivers", "nvme")
	if err := os.MkdirAll(driverDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(driverDir, filepath.Join(dir, "driver")); err != nil {
		t.Fatal(err)
	}
	ctx.Device.VendorID, ctx.Device.DeviceID = 0x8086, 0xf1a6
	writeFile(t, dir, "device", "0xf1a6\n")
	binary.LittleEndian.PutUint64(data, 0x0010003078013fff)
	binary.LittleEndian.PutUint32(data[0x14:], 0x00460001)
	binary.LittleEndian.PutUint32(data[0x1c:], 1)
	binary.LittleEndian.PutUint32(data[0x3c:], 0)
	if cmb {
		binary.LittleEndian.PutUint32(data[0x3c:], 0x1001)
		binary.LittleEndian.PutUint32(data[0x38:], 0x3000)
	}
	if err := os.WriteFile(filepath.Join(dir, "resource0"), data, 0600); err != nil {
		t.Fatal(err)
	}
	ctx.BARContents[0] = append([]byte(nil), data[:56]...)
	c.options.NVMeOptionalSnapshot = true
	return c, ctx, dir, data
}

func TestOptionalSnapshotDefaultDoesNotAccessDevice(t *testing.T) {
	if err := (&Collector{}).collectNVMeOptionalSnapshot(nil); err != nil {
		t.Fatal(err)
	}
}

func TestOptionalSnapshotBackendAndJSONIsolation(t *testing.T) {
	for _, cmb := range []bool{false, true} {
		c, ctx, dir, original := nvmeOptionalFixture(t, cmb)
		before, err := ctx.ToJSON()
		if err != nil {
			t.Fatal(err)
		}
		if err := c.collectNVMeOptionalSnapshot(ctx); err != nil {
			t.Fatal(err)
		}
		if ctx.NVMeOptional == nil || ctx.NVMeOptional.CMB.Supported != cmb {
			t.Fatal("missing or wrong diagnostic snapshot")
		}
		count := 1
		if cmb {
			count = 2
		}
		if len(ctx.NVMeOptional.Registers) != count {
			t.Fatal("extra optional reads")
		}
		raw, err := ctx.ToJSON()
		if err != nil {
			t.Fatal(err)
		}
		loaded, err := FromJSON(raw)
		if err != nil {
			t.Fatal(err)
		}
		want, _ := json.Marshal(ctx.NVMeOptional)
		got, _ := json.Marshal(loaded.NVMeOptional)
		if !bytes.Equal(want, got) {
			t.Fatal("snapshot lost on roundtrip")
		}
		ctx.NVMeOptional = nil
		after, _ := ctx.ToJSON()
		if !bytes.Equal(before, after) {
			t.Fatal("optional capture changed baseline/model/identity data")
		}
		hardware, err := os.ReadFile(filepath.Join(dir, "resource0"))
		if err != nil || !bytes.Equal(hardware, original) {
			t.Fatal("optional capture wrote to resource")
		}
	}
}

func TestOptionalBackendRejectsBeforeMapping(t *testing.T) {
	for _, tc := range []string{"class", "config", "resource", "driver", "short-file", "MSE-off", "identity-mismatch", "version", "not-ready"} {
		t.Run(tc, func(t *testing.T) {
			c, ctx, dir, _ := nvmeOptionalFixture(t, false)
			switch tc {
			case "short-file":
				if err := os.Truncate(filepath.Join(dir, "resource0"), 0x3f); err != nil {
					t.Fatal(err)
				}
			case "MSE-off":
				ctx.ConfigSpace.WriteU16(4, 0)
				if err := os.WriteFile(filepath.Join(dir, "config"), ctx.ConfigSpace.Data[:], 0600); err != nil {
					t.Fatal(err)
				}
			case "identity-mismatch":
				ctx.Device.DeviceID = 0x1234
			case "version":
				binary.LittleEndian.PutUint32(ctx.BARContents[0][8:], 0x10400)
			case "not-ready":
				binary.LittleEndian.PutUint32(ctx.BARContents[0][0x1c:], 0)
			default:
				if err := os.Remove(filepath.Join(dir, tc)); err != nil {
					t.Fatal(err)
				}
			}
			if err := c.collectNVMeOptionalSnapshot(ctx); err == nil || ctx.NVMeOptional != nil {
				t.Fatal("bad preconditions accepted")
			}
		})
	}
}

func TestOptionalBootPreflightAndConditionalRead(t *testing.T) {
	c, ctx, dir, data := nvmeOptionalFixture(t, false)
	cap := binary.LittleEndian.Uint64(ctx.BARContents[0]) | uint64(1)<<45
	binary.LittleEndian.PutUint64(ctx.BARContents[0], cap)
	binary.LittleEndian.PutUint32(data[0x40:], 2)
	if err := os.WriteFile(filepath.Join(dir, "resource0"), data[:0x44], 0600); err != nil {
		t.Fatal(err)
	}
	if err := c.collectNVMeOptionalSnapshot(ctx); err != nil {
		t.Fatal(err)
	}
	if !ctx.NVMeOptional.Boot.Supported || ctx.NVMeOptional.Boot.Size != 262144 || len(ctx.NVMeOptional.Registers) != 2 {
		t.Fatal("wrong conditional boot snapshot")
	}
}

func TestOptionalInvalidSizeDiscardsSnapshot(t *testing.T) {
	c, ctx, dir, data := nvmeOptionalFixture(t, false)
	binary.LittleEndian.PutUint32(data[0x3c:], 0xffffffff)
	if err := os.WriteFile(filepath.Join(dir, "resource0"), data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := c.collectNVMeOptionalSnapshot(ctx); err == nil || ctx.NVMeOptional != nil {
		t.Fatal("all-ones published")
	}
	if _, err := (*SysfsReader)(nil).ReadNVMeOptionalSnapshot(pci.BDF{}, nil, 0, 0); err == nil {
		t.Fatal("missing baseline accepted")
	}
}

func TestCollectorOptionalOptionWiring(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		c, initial, dir, data := nvmeOptionalFixture(t, false)
		vfio.SetSysfsBase(c.sysfs.basePath)
		func() {
			defer vfio.ResetSysfsBase()
			// Use a deliberately nonexistent controller device-node name so
			// identity sysfs works without any ioctl or native-driver visit.
			ctrl := filepath.Join(dir, "nvme", "nvme_pcileechgen_optional_test_only")
			if err := os.MkdirAll(ctrl, 0755); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"model", "serial", "firmware_rev"} {
				writeFile(t, ctrl, name, "test")
			}
			if !enabled {
				if err := os.WriteFile(filepath.Join(dir, "resource0"), data[:56], 0600); err != nil {
					t.Fatal(err)
				}
			}
			c.options.NVMeOptionalSnapshot = enabled
			ctx, err := c.Collect(initial.Device.BDF)
			if err != nil {
				t.Fatal(err)
			}
			if (ctx.NVMeOptional != nil) != enabled || len(ctx.BARContents[0]) != 56 || len(ctx.BARProfiles[0].Probes) != 12 {
				t.Fatal("optional wiring changed baseline policy or lost snapshot")
			}
		}()
	}
}
