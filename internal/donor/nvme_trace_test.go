package donor

import (
	"encoding/json"
	"github.com/sercanarga/pcileechgen/internal/donor/nvmetrace"
	"reflect"
	"testing"
)

func TestTraceContextRoundTrip(t *testing.T) {
	c := &DeviceContext{NVMeTrace: &nvmetrace.Evidence{Policy: "nvme-tracepoint-evidence-v1", Controller: "nvme0", MMIOObserved: false, Queues: []nvmetrace.Queue{{QID: 0, SQOffset: 0x1000, CQOffset: 0x1004}}}}
	data, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	var got DeviceContext
	if err = json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(c.NVMeTrace, got.NVMeTrace) {
		t.Fatal("trace evidence lost")
	}
}
