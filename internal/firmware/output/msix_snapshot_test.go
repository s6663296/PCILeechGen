package output

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/sercanarga/pcileechgen/internal/donor"
	"github.com/sercanarga/pcileechgen/internal/donor/baraccess"
	"github.com/sercanarga/pcileechgen/internal/firmware/codegen"
	"github.com/sercanarga/pcileechgen/internal/firmware/svgen"
	"github.com/sercanarga/pcileechgen/internal/pci"
)

func TestMSIXDiagnosticSnapshotNeverBecomesFirmwareAddresses(t *testing.T) {
	ctx := &donor.DeviceContext{MSIXData: &donor.MSIXData{
		TableSize: 16, TableOffset: 0x2000, PBAOffset: 0x2100,
		Snapshot: &donor.MSIXSnapshot{ReadPolicy: baraccess.NVMeMSIXReadPolicy, Mode: "all",
			Ranges: []donor.MSIXRangeSnapshot{
				{MSIXReadRange: baraccess.MSIXReadRange{Kind: "table", Offset: 0x2000, Length: 256}, Data: bytes.Repeat([]byte{0xa5}, 256)},
				{MSIXReadRange: baraccess.MSIXReadRange{Kind: "pba", Offset: 0x2100, Length: 8}, Data: bytes.Repeat([]byte{0xff}, 8)},
			}},
	}}
	cfg := &svgen.SVGeneratorConfig{MSIXConfig: &svgen.MSIXConfig{NumVectors: 16, TableOffset: 0x2000, PBAOffset: 0x2100}}
	out := t.TempDir()
	if err := NewOutputWriter(out, "unused", 1, 1).writeConditionalArtifacts(cfg, ctx); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(out, "msix_table_init.hex"))
	if err != nil {
		t.Fatal(err)
	}
	want := codegen.GenerateMSIXTableHex(make([]pci.MSIXEntry, 16))
	if string(got) != want {
		t.Fatal("captured host MSI-X state leaked into firmware reset table")
	}
	if ctx.MSIXData.Entries != nil || ctx.MSIXData.Snapshot.Ranges[0].Data[0] != 0xa5 {
		t.Fatal("output generation mutated diagnostic capture")
	}
}
