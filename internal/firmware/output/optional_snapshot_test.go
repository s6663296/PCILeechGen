package output

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/sercanarga/pcileechgen/internal/donor/baraccess"
)

func TestOptionalDiagnosticSnapshotDoesNotChangeGeneratedDeviceModel(t *testing.T) {
	ctx := outputModelContext()
	out := t.TempDir()
	w := NewOutputWriter(out, "unused", 1, 1)
	if err := w.writeDeviceModel(ctx, nil, nil); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(out, "device_model.json"))
	if err != nil {
		t.Fatal(err)
	}
	ctx.NVMeOptional = &baraccess.OptionalSnapshot{
		ReadPolicy: baraccess.NVMeOptionalReadPolicy,
		Registers:  []baraccess.OptionalRegister{{Name: "CMBSZ", Offset: 0x3c, Value: 0x1001}, {Name: "CMBLOC", Offset: 0x38, Value: 0x3002}},
		CMB:        baraccess.CMBDescriptor{Supported: true, BIR: 2, Offset: 0x3000, Size: 4096},
	}
	if err := w.writeDeviceModel(ctx, nil, nil); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(filepath.Join(out, "device_model.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("diagnostic descriptors changed generated device model")
	}
}
