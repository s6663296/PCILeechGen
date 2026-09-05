package baraccess

import (
	"encoding/binary"
	"fmt"
)

const NVMeOptionalReadPolicy = "nvme-1.3-optional-descriptors-v1"

// OptionalSnapshot is diagnostic evidence, not a BAR reset image. Only the
// Registers list contains measured values; skipped registers are never zeroed
// and presented as measurements. See NVMe 1.3 sections 3.1.11-3.1.15.
type OptionalSnapshot struct {
	ReadPolicy string             `json:"read_policy"`
	Version    uint32             `json:"version"`
	CAP        string             `json:"cap"`
	Registers  []OptionalRegister `json:"registers"`
	CMB        CMBDescriptor      `json:"cmb"`
	Boot       BootDescriptor     `json:"boot_partition"`
	Skipped    []OptionalSkip     `json:"skipped"`
}

type OptionalRegister struct {
	Name   string `json:"name"`
	Offset uint32 `json:"offset"`
	Value  uint32 `json:"value"`
}

type OptionalSkip struct {
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

type CMBDescriptor struct {
	Supported bool   `json:"supported"`
	BIR       uint32 `json:"bir,omitempty"`
	Offset    uint64 `json:"offset_bytes,omitempty"`
	Size      uint64 `json:"reported_size_bytes,omitempty"`
	SizeUnit  uint64 `json:"size_unit_bytes,omitempty"`
	SQS       bool   `json:"submission_queues"`
	CQS       bool   `json:"completion_queues"`
	LISTS     bool   `json:"prp_sgl_lists"`
	RDS       bool   `json:"read_data"`
	WDS       bool   `json:"write_data"`
}

type BootDescriptor struct {
	Supported  bool   `json:"supported"`
	Size       uint64 `json:"partition_size_bytes,omitempty"`
	ActiveID   uint32 `json:"active_id"`
	ReadStatus uint32 `json:"read_status"`
}

// OptionalExtent validates the captured baseline before any new access. This
// first policy is intentionally restricted to exactly the reviewed NVMe 1.3
// version, not speculative compatibility with future versions.
func OptionalExtent(baseline []byte) (int, error) {
	if len(baseline) < NVMeSnapshotSize {
		return 0, fmt.Errorf("optional snapshot requires a complete baseline snapshot")
	}
	cap := binary.LittleEndian.Uint64(baseline[:8])
	vs := binary.LittleEndian.Uint32(baseline[8:12])
	if cap == 0 || cap == ^uint64(0) || vs != 0x00010300 {
		return 0, fmt.Errorf("optional register policy requires valid CAP and NVMe 1.3 VS; got CAP=%#x VS=%#x", cap, vs)
	}
	cc := binary.LittleEndian.Uint32(baseline[0x14:0x18])
	csts := binary.LittleEndian.Uint32(baseline[0x1c:0x20])
	if cc&1 == 0 || csts&0x2f != 1 {
		return 0, fmt.Errorf("optional snapshot requires an enabled, ready controller without fatal, shutdown or paused status")
	}
	if cap&(uint64(1)<<45) != 0 {
		return 0x44, nil // BPINFO only; never BPRSEL/BPMBL
	}
	return 0x40, nil
}

// ReadNVMeOptional reads CMBSZ first, conditionally CMBLOC, and BPINFO only
// when CAP.BPS is set. It accepts no arbitrary register offsets and never follows
// a CMB pointer or initiates a boot read. read32 must be an exact DWORD accessor.
func ReadNVMeOptional(baseline []byte, read32 func(int) (uint32, error)) (*OptionalSnapshot, error) {
	if _, err := OptionalExtent(baseline); err != nil {
		return nil, err
	}
	if read32 == nil {
		return nil, fmt.Errorf("optional snapshot requires a DWORD reader")
	}
	cap := binary.LittleEndian.Uint64(baseline[:8])
	s := &OptionalSnapshot{ReadPolicy: NVMeOptionalReadPolicy, Version: 0x00010300,
		CAP: fmt.Sprintf("0x%016X", cap), Skipped: []OptionalSkip{
			{Name: "NSSR", Reason: "reset control excluded even if CAP.NSSRS is set"},
			{Name: "BPRSEL/BPMBL", Reason: "boot-transfer control/address registers excluded"},
			{Name: "CMB memory, reserved/vendor regions and doorbells", Reason: "not part of the descriptor-only policy"},
		}}
	read := func(name string, off int) (uint32, error) {
		value, err := read32(off)
		if err != nil {
			return 0, fmt.Errorf("optional %s DWORD at %#x: %w", name, off, err)
		}
		if value == ^uint32(0) {
			return 0, fmt.Errorf("optional %s DWORD at %#x returned all ones; stopping", name, off)
		}
		s.Registers = append(s.Registers, OptionalRegister{Name: name, Offset: uint32(off), Value: value})
		return value, nil
	}
	sz, err := read("CMBSZ", 0x3c)
	if err != nil {
		return nil, err
	}
	if sz == 0 {
		s.Skipped = append(s.Skipped, OptionalSkip{Name: "CMBLOC", Reason: "CMBSZ=0: CMB unsupported, CMBLOC is reserved"})
	} else {
		units := (sz >> 8) & 15
		if units > 6 || sz&0xe0 != 0 || sz>>12 == 0 || (sz&4 != 0 && sz&1 == 0) {
			return nil, fmt.Errorf("invalid NVMe 1.3 CMBSZ=%#x; refusing CMBLOC read", sz)
		}
		loc, err := read("CMBLOC", 0x38)
		if err != nil {
			return nil, err
		}
		bir := loc & 7
		if loc&0xff8 != 0 || bir == 1 || bir > 5 {
			return nil, fmt.Errorf("invalid NVMe 1.3 CMBLOC=%#x", loc)
		}
		unit := uint64(1) << (12 + 4*units)
		s.CMB = CMBDescriptor{Supported: true, BIR: bir, SizeUnit: unit,
			Offset: uint64(loc>>12) * unit, Size: uint64(sz>>12) * unit,
			SQS: sz&1 != 0, CQS: sz&2 != 0, LISTS: sz&4 != 0, RDS: sz&8 != 0, WDS: sz&16 != 0}
	}
	s.Boot.Supported = cap&(uint64(1)<<45) != 0
	if s.Boot.Supported {
		info, err := read("BPINFO", 0x40)
		if err != nil {
			return nil, err
		}
		if info&0x7cff8000 != 0 {
			return nil, fmt.Errorf("BPINFO contains reserved NVMe 1.3 bits: %#x", info)
		}
		s.Boot.Size = uint64(info&0x7fff) * 128 * 1024
		s.Boot.ActiveID, s.Boot.ReadStatus = info>>31, (info>>24)&3
	} else {
		s.Skipped = append(s.Skipped, OptionalSkip{Name: "BPINFO", Reason: "CAP.BPS=0: Boot Partitions unsupported"})
	}
	return s, nil
}
