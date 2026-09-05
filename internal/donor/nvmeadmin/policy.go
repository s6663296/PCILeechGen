package nvmeadmin

import (
	"encoding/binary"
	"fmt"
)

const (
	ReadPolicy       = "nvme-1.3-native-readonly-v1"
	MaxNamespaces    = 4096
	OpcodeGetLogPage = uint8(0x02)
	OpcodeIdentify   = uint8(0x06)
)

type Snapshot struct {
	ReadPolicy        string              `json:"read_policy"`
	ControllerVersion uint32              `json:"controller_version"`
	Capabilities      Capabilities        `json:"capabilities"`
	Namespaces        []NamespaceSnapshot `json:"namespaces"`
	LogPages          []LogPageSnapshot   `json:"log_pages"`
	Skipped           []Skip              `json:"skipped,omitempty"`
}
type Capabilities struct {
	OACS    uint16 `json:"oacs"`
	LPA     uint8  `json:"lpa"`
	ELPE    uint8  `json:"elpe"`
	NN      uint32 `json:"nn"`
	ONCS    uint16 `json:"oncs"`
	SANICAP uint32 `json:"sanicap"`
}
type NamespaceSnapshot struct {
	NSID     uint32 `json:"nsid"`
	Identify []byte `json:"identify"`
}
type LogPageSnapshot struct {
	Name string `json:"name"`
	LID  uint8  `json:"lid"`
	NSID uint32 `json:"nsid"`
	RAE  bool   `json:"retain_async_event"`
	Data []byte `json:"data"`
}
type Skip struct {
	Name   string `json:"name"`
	Reason string `json:"reason"`
}
type Request struct {
	Name   string
	Opcode uint8
	NSID   uint32
	CNS    uint32
	LID    uint8
	Length int
	RAE    bool
}

func IdentifyRequest(name string, cns, nsid uint32) Request {
	return Request{Name: name, Opcode: OpcodeIdentify, CNS: cns, NSID: nsid, Length: 4096}
}
func LogRequest(name string, lid uint8, length int) Request {
	return Request{Name: name, Opcode: OpcodeGetLogPage, NSID: 0xffffffff, LID: lid, Length: length, RAE: true}
}

func CommandDwords(r Request) (uint32, uint32, error) {
	if r.Length <= 0 || r.Length%4 != 0 {
		return 0, 0, fmt.Errorf("request %q requires a non-zero DWORD-aligned buffer", r.Name)
	}
	switch r.Opcode {
	case OpcodeIdentify:
		return r.CNS, 0, nil
	case OpcodeGetLogPage:
		n := uint32(r.Length/4 - 1)
		cdw10 := uint32(r.LID) | (n&0xffff)<<16
		if r.RAE {
			cdw10 |= 1 << 15
		}
		return cdw10, n >> 16, nil
	default:
		return 0, 0, fmt.Errorf("opcode %#x is outside the read-only policy", r.Opcode)
	}
}

type Executor func(Request) ([]byte, error)

func Capture(raw []byte, exec Executor) (*Snapshot, error) {
	if len(raw) != 4096 || exec == nil {
		return nil, fmt.Errorf("native snapshot requires a complete raw Identify Controller and executor")
	}
	version := binary.LittleEndian.Uint32(raw[0x50:])
	if version != 0x00010300 {
		return nil, fmt.Errorf("native snapshot policy requires NVMe 1.3.0 Identify VER; got %#x", version)
	}
	c := Capabilities{OACS: binary.LittleEndian.Uint16(raw[0x100:]), LPA: raw[0x105], ELPE: raw[0x106], NN: binary.LittleEndian.Uint32(raw[0x204:]), ONCS: binary.LittleEndian.Uint16(raw[0x208:]), SANICAP: binary.LittleEndian.Uint32(raw[0x148:])}
	if c.NN == 0 || c.NN > MaxNamespaces {
		return nil, fmt.Errorf("Identify Controller NN=%d is outside policy limit 1..%d", c.NN, MaxNamespaces)
	}
	s := &Snapshot{ReadPolicy: ReadPolicy, ControllerVersion: version, Capabilities: c}
	list, err := exec(IdentifyRequest("Active Namespace List", 2, 0))
	if err != nil {
		return nil, fmt.Errorf("Identify Active Namespace List: %w", err)
	}
	if len(list) != 4096 {
		return nil, fmt.Errorf("Active Namespace List returned %d bytes", len(list))
	}
	last := uint32(0)
	for off := 0; off < len(list); off += 4 {
		nsid := binary.LittleEndian.Uint32(list[off:])
		if nsid == 0 {
			break
		}
		if nsid <= last || uint32(len(s.Namespaces)) >= c.NN {
			return nil, fmt.Errorf("invalid Active Namespace List entry %#x after %#x", nsid, last)
		}
		id, readErr := exec(IdentifyRequest(fmt.Sprintf("Identify Namespace %d", nsid), 0, nsid))
		if readErr != nil {
			return nil, fmt.Errorf("Identify Namespace %d: %w", nsid, readErr)
		}
		if len(id) != 4096 {
			return nil, fmt.Errorf("Identify Namespace %d returned %d bytes", nsid, len(id))
		}
		s.Namespaces = append(s.Namespaces, NamespaceSnapshot{NSID: nsid, Identify: id})
		last = nsid
	}
	if uint32(len(s.Namespaces)) < c.NN && binary.LittleEndian.Uint32(list[4092:]) != 0 {
		return nil, fmt.Errorf("more than 1024 active namespaces require pagination, excluded by this policy revision")
	}

	requests := []Request{LogRequest("Error Information", 0x01, (int(c.ELPE)+1)*64), LogRequest("SMART / Health Information", 0x02, 512), LogRequest("Firmware Slot Information", 0x03, 512)}
	if c.OACS&(1<<3) != 0 {
		requests = append(requests, LogRequest("Changed Namespace List", 0x04, 4096))
	} else {
		s.Skipped = append(s.Skipped, Skip{"Changed Namespace List", "OACS.Namespace Management=0"})
	}
	if c.LPA&(1<<1) != 0 {
		requests = append(requests, LogRequest("Commands Supported and Effects", 0x05, 4096))
	} else {
		s.Skipped = append(s.Skipped, Skip{"Commands Supported and Effects", "LPA.CELP=0"})
	}
	if c.OACS&(1<<4) != 0 {
		requests = append(requests, LogRequest("Device Self-test", 0x06, 564))
	} else {
		s.Skipped = append(s.Skipped, Skip{"Device Self-test", "OACS.Device Self-test=0"})
	}
	if c.ONCS&(1<<5) != 0 {
		requests = append(requests, LogRequest("Reservation Notification", 0x80, 64))
	} else {
		s.Skipped = append(s.Skipped, Skip{"Reservation Notification", "ONCS.Reservations=0"})
	}
	if c.SANICAP != 0 {
		requests = append(requests, LogRequest("Sanitize Status", 0x81, 512))
	} else {
		s.Skipped = append(s.Skipped, Skip{"Sanitize Status", "SANICAP=0"})
	}
	s.Skipped = append(s.Skipped, Skip{"Telemetry logs 0x07/0x08", "variable-size/stateful telemetry excluded from native-readonly-v1"}, Skip{"Vendor-specific logs", "no specification-derived safe length or semantics"})
	for _, req := range requests {
		data, readErr := exec(req)
		if readErr != nil {
			return nil, fmt.Errorf("Get Log Page %s (LID=%#x): %w", req.Name, req.LID, readErr)
		}
		if len(data) != req.Length {
			return nil, fmt.Errorf("Get Log Page %s returned %d bytes, expected %d", req.Name, len(data), req.Length)
		}
		s.LogPages = append(s.LogPages, LogPageSnapshot{Name: req.Name, LID: req.LID, NSID: req.NSID, RAE: req.RAE, Data: data})
	}
	return s, nil
}
