package output

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/sercanarga/pcileechgen/internal/donor/nvmetrace"
)

func TestTraceEvidenceDoesNotChangeOperationalModel(t *testing.T) {
	ctx := outputModelContext()
	dir := t.TempDir()
	w := NewOutputWriter(dir, "unused", 1, 1)
	if err := w.writeDeviceModel(ctx, nil, nil); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(dir, "device_model.json"))
	if err != nil {
		t.Fatal(err)
	}
	ctx.NVMeTrace = &nvmetrace.Evidence{Policy: "nvme-tracepoint-evidence-v1", Controller: "nvme0", Queues: []nvmetrace.Queue{{QID: 0, SQOffset: 0x1000, CQOffset: 0x1004, Samples: []nvmetrace.Sample{{Head: 20, Tail: 21}}}}}
	if err = w.writeDeviceModel(ctx, nil, nil); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(filepath.Join(dir, "device_model.json"))
	if err != nil {
		t.Fatal(err)
	}
	var a, b map[string]json.RawMessage
	if err = json.Unmarshal(before, &a); err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(after, &b); err != nil {
		t.Fatal(err)
	}
	if _, ok := b["nvme_trace_evidence"]; !ok {
		t.Fatal("missing evidence")
	}
	delete(b, "nvme_trace_evidence")
	aa, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	if !bytes.Equal(aa, bb) {
		t.Fatal("trace changed operational device model")
	}
}
