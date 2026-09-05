package donor

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sercanarga/pcileechgen/internal/donor/baraccess"
	"github.com/sercanarga/pcileechgen/internal/donor/vfio"
	"github.com/sercanarga/pcileechgen/internal/pci"
)

func nvmeMSIXFixture(t *testing.T) (*Collector, *DeviceContext, string, []byte) {
	t.Helper()
	c, bdf, dir, original := nvmeBARFixture(t)
	cs := pci.NewConfigSpace()
	cs.WriteU32(0, 0xf1a68086)
	cs.WriteU16(4, 0x02)
	cs.WriteU32(8, 0x01080203)
	cs.WriteU16(6, 0x10)
	cs.WriteU8(0x34, 0x50)
	cs.WriteU8(0x50, pci.CapIDMSIX)
	cs.WriteU16(0x52, 15)
	cs.WriteU32(0x54, 0x2000)
	cs.WriteU32(0x58, 0x2100)
	if err := os.WriteFile(filepath.Join(dir, "config"), cs.Data[:], 0600); err != nil {
		t.Fatal(err)
	}
	// Unique DWORDs ensure a page-offset bug or accidental gap read cannot
	// pass by seeing identical filler at the wrong address.
	for off := 0x2000; off < 0x6000; off += 4 {
		binary.LittleEndian.PutUint32(original[off:off+4], 0xa5000000|uint32(off))
	}
	if err := os.WriteFile(filepath.Join(dir, "resource0"), original, 0600); err != nil {
		t.Fatal(err)
	}
	data, err := c.sysfs.ReadBARContent(bdf, 0, 65536)
	if err != nil {
		t.Fatal(err)
	}
	ctx := &DeviceContext{Device: pci.PCIDevice{BDF: bdf, ClassCode: 0x010802}, ConfigSpace: cs,
		BARContents: map[int][]byte{0: data}, MSIXData: c.collectMSIXData(cs, nil)}
	return c, ctx, dir, original
}

func TestNVMeMSIXSnapshotIsDiagnosticAndRoundTrips(t *testing.T) {
	for _, mode := range []baraccess.MSIXSnapshotMode{baraccess.MSIXSnapshotTable, baraccess.MSIXSnapshotPBA, baraccess.MSIXSnapshotAll} {
		t.Run(string(mode), func(t *testing.T) {
			c, ctx, dir, original := nvmeMSIXFixture(t)
			baseline := append([]byte(nil), ctx.BARContents[0]...)
			if err := c.collectNVMeMSIXSnapshot(ctx, mode); err != nil {
				t.Fatal(err)
			}
			snapshot := ctx.MSIXData.Snapshot
			if snapshot == nil || snapshot.Mode != string(mode) || snapshot.ReadPolicy != baraccess.NVMeMSIXReadPolicy || snapshot.CollectedAt.IsZero() {
				t.Fatalf("invalid snapshot: %+v", snapshot)
			}
			wantRanges := 1
			if mode == baraccess.MSIXSnapshotAll {
				wantRanges = 2
			}
			if len(snapshot.Ranges) != wantRanges {
				t.Fatal("unexpected selected ranges")
			}
			for _, r := range snapshot.Ranges {
				if !bytes.Equal(r.Data, original[r.Offset:r.End()]) {
					t.Fatal("incorrect range data")
				}
			}
			if ctx.MSIXData.Entries != nil || !bytes.Equal(ctx.BARContents[0], baseline) || len(baseline) != 56 {
				t.Fatal("diagnostic snapshot contaminated firmware initialization")
			}
			raw, err := ctx.ToJSON()
			if err != nil {
				t.Fatal(err)
			}
			loaded, err := FromJSON(raw)
			if err != nil {
				t.Fatal(err)
			}
			again, err := json.Marshal(loaded.MSIXData.Snapshot)
			want, _ := json.Marshal(snapshot)
			if err != nil || !bytes.Equal(want, again) {
				t.Fatal("snapshot JSON roundtrip changed data or geometry")
			}
			after, err := os.ReadFile(filepath.Join(dir, "resource0"))
			if err != nil || !bytes.Equal(after, original) {
				t.Fatal("read-only snapshot wrote to BAR")
			}
		})
	}
}

func TestNVMeMSIXOffDoesNotTouchSysfs(t *testing.T) {
	c := &Collector{} // nil reader; any hardware access would panic
	if err := c.collectNVMeMSIXSnapshot(nil, baraccess.MSIXSnapshotOff); err != nil {
		t.Fatal(err)
	}
	if snapshot, err := (*SysfsReader)(nil).ReadNVMeMSIXSnapshot(pci.BDF{}, nil, baraccess.MSIXSnapshotOff); err != nil || snapshot != nil {
		t.Fatal("off mode accessed metadata")
	}
	c.options.NVMeMSIXSnapshot = "invalid"
	if _, err := c.Collect(pci.BDF{}); err == nil {
		t.Fatal("invalid mode accepted")
	}
}

func TestCollectorNVMeMSIXOptionWiring(t *testing.T) {
	for _, mode := range []string{"", "all"} {
		t.Run("mode="+mode, func(t *testing.T) {
			c, initial, dir, _ := nvmeMSIXFixture(t)
			// Route VFIO metadata/power helpers to the same fake sysfs. Never
			// inspect or wake a real device while exercising the full Collector.
			vfio.SetSysfsBase(c.sysfs.basePath)
			t.Cleanup(vfio.ResetSysfsBase)
			ctrl := filepath.Join(dir, "nvme", "nvme_pcileechgen_unit_test_only")
			if err := os.MkdirAll(ctrl, 0755); err != nil {
				t.Fatal(err)
			}
			for name, value := range map[string]string{"model": "mock", "serial": "mock", "firmware_rev": "mock"} {
				writeFile(t, ctrl, name, value)
			}
			// The ioctl helper cannot open this non-kernel controller name, but
			// sysfs identity succeeds so no native-driver visit is attempted.
			if mode == "" {
				if err := os.Truncate(filepath.Join(dir, "resource0"), 56); err != nil {
					t.Fatal(err)
				}
			}
			c.options.NVMeMSIXSnapshot = mode
			ctx, err := c.Collect(initial.Device.BDF)
			if err != nil {
				t.Fatal(err)
			}
			if (ctx.MSIXData.Snapshot != nil) != (mode == "all") {
				t.Fatal("collector ignored option or attempted capture by default")
			}
			if len(ctx.BARContents[0]) != 56 || len(ctx.BARProfiles[0].Probes) != 12 || ctx.MSIXData.Entries != nil {
				t.Fatal("experimental option changed baseline/profiling/initialization semantics")
			}
		})
	}
}

func TestNVMeMSIXRejectsChangedLayoutBeforeBAROpen(t *testing.T) {
	c, ctx, dir, _ := nvmeMSIXFixture(t)
	if err := os.Remove(filepath.Join(dir, "resource0")); err != nil {
		t.Fatal(err)
	}
	expected := pci.ParseMSIXCap(ctx.ConfigSpace)
	expected.PBAOffset = 0x2200
	if _, err := c.sysfs.ReadNVMeMSIXSnapshot(ctx.Device.BDF, expected, baraccess.MSIXSnapshotAll); err == nil || !strings.Contains(err.Error(), "layout changed") {
		t.Fatalf("did not reject metadata before resource access: %v", err)
	}
}

func TestNVMeMSIXRejectsTruncatedResourceBeforePartialCapture(t *testing.T) {
	c, ctx, dir, _ := nvmeMSIXFixture(t)
	// Table fits, PBA does not. The all-mode preflight must reject the entire
	// request rather than publishing a successful table and missing PBA.
	if err := os.Truncate(filepath.Join(dir, "resource0"), 0x2100); err != nil {
		t.Fatal(err)
	}
	if err := c.collectNVMeMSIXSnapshot(ctx, baraccess.MSIXSnapshotAll); err == nil || !strings.Contains(err.Error(), "file size") {
		t.Fatalf("expected preflight error, got %v", err)
	}
	if ctx.MSIXData.Snapshot != nil {
		t.Fatal("partial capture published")
	}
}

func TestNVMeMSIXMmapOffsetIsPageRelative(t *testing.T) {
	c, ctx, dir, original := nvmeMSIXFixture(t)
	ctx.ConfigSpace.WriteU32(0x54, 0x2ff8) // table crosses a page boundary
	ctx.ConfigSpace.WriteU32(0x58, 0x5008) // PBA is not at its page start
	if err := os.WriteFile(filepath.Join(dir, "config"), ctx.ConfigSpace.Data[:], 0600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := c.sysfs.ReadNVMeMSIXSnapshot(ctx.Device.BDF, pci.ParseMSIXCap(ctx.ConfigSpace), baraccess.MSIXSnapshotAll)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range snapshot.Ranges {
		if !bytes.Equal(r.Data, original[r.Offset:r.End()]) {
			t.Fatalf("wrong page-relative data at %#x", r.Offset)
		}
	}
}

func TestNVMeMSIXRejectsBadMetadata(t *testing.T) {
	for _, tc := range []string{"class", "config", "resource", "non-nvme", "no-msix", "MSE-off"} {
		t.Run(tc, func(t *testing.T) {
			c, ctx, dir, _ := nvmeMSIXFixture(t)
			switch tc {
			case "non-nvme":
				writeFile(t, dir, "class", "0x020000")
			case "no-msix":
				ctx.ConfigSpace.WriteU8(0x34, 0)
				if err := os.WriteFile(filepath.Join(dir, "config"), ctx.ConfigSpace.Data[:], 0600); err != nil {
					t.Fatal(err)
				}
			case "MSE-off":
				ctx.ConfigSpace.WriteU16(4, 0)
				if err := os.WriteFile(filepath.Join(dir, "config"), ctx.ConfigSpace.Data[:], 0600); err != nil {
					t.Fatal(err)
				}
			default:
				if err := os.Remove(filepath.Join(dir, tc)); err != nil {
					t.Fatal(err)
				}
			}
			if err := c.collectNVMeMSIXSnapshot(ctx, baraccess.MSIXSnapshotAll); err == nil {
				t.Fatal("invalid metadata allowed live snapshot")
			}
		})
	}
}
