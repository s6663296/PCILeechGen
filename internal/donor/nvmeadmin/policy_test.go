package nvmeadmin

import (
	"bytes"
	"encoding/binary"
	"errors"
	"reflect"
	"testing"
)

func controllerFixture() []byte {
	raw := make([]byte, 4096)
	binary.LittleEndian.PutUint32(raw[0x50:], 0x00010300)
	binary.LittleEndian.PutUint16(raw[0x100:], 0x0017)
	raw[0x105], raw[0x106] = 0x0f, 255
	binary.LittleEndian.PutUint32(raw[0x204:], 1)
	binary.LittleEndian.PutUint16(raw[0x208:], 0x005f)
	binary.LittleEndian.PutUint32(raw[0x148:], 3)
	return raw
}

func TestCaptureExactCapabilityGatedSequence(t *testing.T) {
	var got []Request
	s, err := Capture(controllerFixture(), func(req Request) ([]byte, error) {
		got = append(got, req)
		data := bytes.Repeat([]byte{byte(len(got))}, req.Length)
		if req.CNS == 2 {
			binary.LittleEndian.PutUint32(data, 1)
			binary.LittleEndian.PutUint32(data[4:], 0)
		}
		return data, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"Active Namespace List", "Identify Namespace 1", "Error Information", "SMART / Health Information", "Firmware Slot Information", "Commands Supported and Effects", "Device Self-test", "Sanitize Status"}
	names := make([]string, len(got))
	for i := range got {
		names[i] = got[i].Name
	}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("requests=%q want=%q", names, want)
	}
	if len(s.Namespaces) != 1 || len(s.LogPages) != 6 || len(s.LogPages[0].Data) != 16384 {
		t.Fatalf("wrong snapshot sizes: namespaces=%d logs=%d error_bytes=%d", len(s.Namespaces), len(s.LogPages), len(s.LogPages[0].Data))
	}
}

func TestCommandDwords(t *testing.T) {
	r := LogRequest("error", 1, 16384)
	cdw10, cdw11, err := CommandDwords(r)
	if err != nil || cdw10 != 0x0fff8001 || cdw11 != 0 {
		t.Fatalf("cdw10=%#x cdw11=%#x err=%v", cdw10, cdw11, err)
	}
	if _, _, err := CommandDwords(Request{Name: "write", Opcode: 1, Length: 4}); err == nil {
		t.Fatal("unsafe opcode accepted")
	}
}

func TestErrorStopsAndDiscardsPartialSnapshot(t *testing.T) {
	count := 0
	s, err := Capture(controllerFixture(), func(req Request) ([]byte, error) {
		count++
		if count == 3 {
			return nil, errors.New("injected")
		}
		data := make([]byte, req.Length)
		if req.CNS == 2 {
			binary.LittleEndian.PutUint32(data, 1)
		}
		return data, nil
	})
	if err == nil || s != nil || count != 3 {
		t.Fatalf("snapshot=%v err=%v count=%d", s, err, count)
	}
}
