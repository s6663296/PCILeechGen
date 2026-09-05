package donor

import (
	"bytes"
	"encoding/binary"
	"errors"
	"reflect"
	"testing"

	"github.com/sercanarga/pcileechgen/internal/donor/nvmeadmin"
	"github.com/sercanarga/pcileechgen/internal/pci"
)

func nativeControllerFixture() []byte {
	raw := make([]byte, 4096)
	binary.LittleEndian.PutUint32(raw[0x50:], 0x00010300)
	binary.LittleEndian.PutUint16(raw[0x100:], 0x0017)
	raw[0x105] = 0x0f
	raw[0x106] = 255
	binary.LittleEndian.PutUint32(raw[0x204:], 1)
	binary.LittleEndian.PutUint16(raw[0x208:], 0x005f)
	binary.LittleEndian.PutUint32(raw[0x148:], 3)
	return raw
}

func TestCaptureNVMeAdminSnapshotCapabilityGatedSequence(t *testing.T) {
	raw := nativeControllerFixture()
	var requests []nvmeadmin.Request
	exec := func(req nvmeadmin.Request) ([]byte, error) {
		requests = append(requests, req)
		data := bytes.Repeat([]byte{byte(len(requests))}, req.Length)
		if req.CNS == 2 {
			binary.LittleEndian.PutUint32(data, 1)
			binary.LittleEndian.PutUint32(data[4:], 0)
		}
		return data, nil
	}
	s, err := nvmeadmin.Capture(raw, exec)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Namespaces) != 1 || s.Namespaces[0].NSID != 1 || len(s.Namespaces[0].Identify) != 4096 {
		t.Fatalf("wrong namespace snapshot: %+v", s.Namespaces)
	}
	wantNames := []string{"Active Namespace List", "Identify Namespace 1", "Error Information", "SMART / Health Information", "Firmware Slot Information", "Commands Supported and Effects", "Device Self-test", "Sanitize Status"}
	gotNames := make([]string, len(requests))
	for i, req := range requests {
		gotNames[i] = req.Name
	}
	if !reflect.DeepEqual(gotNames, wantNames) {
		t.Fatalf("request sequence=%q, want %q", gotNames, wantNames)
	}
	if len(s.LogPages) != 6 || len(s.LogPages[0].Data) != 16384 {
		t.Fatalf("wrong capability-gated logs: %+v", s.LogPages)
	}
	for _, log := range s.LogPages {
		if !log.RAE || log.NSID != 0xffffffff {
			t.Fatalf("log request did not preserve async event: %+v", log)
		}
	}
}

func TestNVMeAdminCommandEncoding(t *testing.T) {
	req := nvmeadmin.LogRequest("Error Information", 1, 16384)
	cdw10, cdw11, err := nvmeadmin.CommandDwords(req)
	if err != nil {
		t.Fatal(err)
	}
	if req.Opcode != 0x02 || req.NSID != 0xffffffff || req.Length != 16384 || cdw10 != 0x0fff8001 || cdw11 != 0 {
		t.Fatalf("wrong Get Log Page command: %+v cdw10=%#x cdw11=%#x", req, cdw10, cdw11)
	}
	id := nvmeadmin.IdentifyRequest("list", 2, 0)
	id10, _, err := nvmeadmin.CommandDwords(id)
	if err != nil || id.Opcode != 0x06 || id10 != 2 || id.Length != 4096 {
		t.Fatalf("wrong Identify command: %+v err=%v", id, err)
	}
}

func TestCaptureNVMeAdminSnapshotStopsWithoutPublishingPartialData(t *testing.T) {
	raw := nativeControllerFixture()
	count := 0
	s, err := nvmeadmin.Capture(raw, func(req nvmeadmin.Request) ([]byte, error) {
		count++
		if count == 3 {
			return nil, errors.New("injected status")
		}
		data := make([]byte, req.Length)
		if req.CNS == 2 {
			binary.LittleEndian.PutUint32(data, 1)
		}
		return data, nil
	})
	if err == nil || s != nil || count != 3 {
		t.Fatalf("partial result published or read continued: snapshot=%v err=%v count=%d", s, err, count)
	}
}

func TestCaptureNVMeAdminSnapshotRejectsBadPreflight(t *testing.T) {
	for _, mutate := range []func([]byte){
		func(raw []byte) { binary.LittleEndian.PutUint32(raw[0x50:], 0x00010400) },
		func(raw []byte) { binary.LittleEndian.PutUint32(raw[0x204:], 0) },
		func(raw []byte) { binary.LittleEndian.PutUint32(raw[0x204:], nvmeadmin.MaxNamespaces+1) },
	} {
		raw := nativeControllerFixture()
		mutate(raw)
		called := false
		if s, err := nvmeadmin.Capture(raw, func(nvmeadmin.Request) ([]byte, error) { called = true; return nil, nil }); err == nil || s != nil || called {
			t.Fatalf("bad preflight accessed device: snapshot=%v err=%v", s, err)
		}
	}
}

func TestNVMeAdminSnapshotJSONRoundTrip(t *testing.T) {
	ctx := &DeviceContext{ConfigSpace: pci.NewConfigSpace(), NVMeAdmin: &nvmeadmin.Snapshot{
		ReadPolicy: nvmeadmin.ReadPolicy, ControllerVersion: 0x10300,
		Namespaces: []nvmeadmin.NamespaceSnapshot{{NSID: 1, Identify: []byte{1, 2, 3}}},
		LogPages:   []nvmeadmin.LogPageSnapshot{{Name: "SMART", LID: 2, NSID: 0xffffffff, RAE: true, Data: []byte{4, 5}}},
	}}
	raw, err := ctx.ToJSON()
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := FromJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ctx.NVMeAdmin, loaded.NVMeAdmin) {
		t.Fatalf("native snapshot lost on roundtrip: %#v", loaded.NVMeAdmin)
	}
}
