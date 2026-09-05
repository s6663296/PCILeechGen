package baraccess

import (
	"encoding/binary"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func optionalBaseline(boot bool) []byte {
	b := make([]byte, NVMeSnapshotSize)
	cap := uint64(0x0010003078013fff) // supplied Intel donor CAP; NSSRS=1, BPS=0
	if boot {
		cap |= uint64(1) << 45
	}
	binary.LittleEndian.PutUint64(b, cap)
	binary.LittleEndian.PutUint32(b[8:], 0x00010300)
	binary.LittleEndian.PutUint32(b[0x14:], 0x00460001)
	binary.LittleEndian.PutUint32(b[0x1c:], 1)
	return b
}

func TestOptionalExactConditionalAccesses(t *testing.T) {
	for _, tc := range []struct {
		name string
		boot bool
		sz   uint32
		want []int
	}{
		{"no CMB no boot", false, 0, []int{0x3c}},
		{"CMB only", false, 0x201f, []int{0x3c, 0x38}},
		{"boot only", true, 0, []int{0x3c, 0x40}},
		{"CMB and boot", true, 0x201f, []int{0x3c, 0x38, 0x40}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			baseline := optionalBaseline(tc.boot)
			before := append([]byte(nil), baseline...)
			var reads []int
			s, err := ReadNVMeOptional(baseline, func(off int) (uint32, error) {
				reads = append(reads, off)
				switch off {
				case 0x3c:
					return tc.sz, nil
				case 0x38:
					return 0x3002, nil // BAR2, three 4 KiB units
				case 0x40:
					return 0x82000002, nil
				default:
					t.Fatalf("unexpected MMIO address %#x", off)
					return 0, nil
				}
			})
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(reads, tc.want) || !reflect.DeepEqual(before, baseline) {
				t.Fatalf("accesses=%#v want=%#v or baseline modified", reads, tc.want)
			}
			if s.ReadPolicy != NVMeOptionalReadPolicy || len(s.Registers) != len(tc.want) || s.CMB.Supported != (tc.sz != 0) || s.Boot.Supported != tc.boot {
				t.Fatalf("incorrect snapshot: %+v", s)
			}
			for i, reg := range s.Registers {
				if reg.Offset != uint32(tc.want[i]) {
					t.Fatal("fabricated register measurement")
				}
			}
			if s.CMB.Supported && (s.CMB.BIR != 2 || s.CMB.Size != 8192 || s.CMB.Offset != 12288 || !s.CMB.WDS || !s.CMB.RDS || !s.CMB.LISTS || !s.CMB.CQS || !s.CMB.SQS) {
				t.Fatalf("bad CMB decoding: %+v", s.CMB)
			}
			if tc.boot && (s.Boot.Size != 262144 || s.Boot.ActiveID != 1 || s.Boot.ReadStatus != 2) {
				t.Fatalf("bad BPINFO decoding: %+v", s.Boot)
			}
			if tc.sz == 0 {
				found := false
				for _, skip := range s.Skipped {
					if skip.Name == "CMBLOC" {
						found = true
					}
				}
				if !found {
					t.Fatal("unsupported CMBLOC not recorded as skipped")
				}
			}
		})
	}
}

func TestOptionalBaselineRejectsBeforeAccess(t *testing.T) {
	for _, edit := range []func([]byte) []byte{
		func(b []byte) []byte { return b[:0x37] },
		func(b []byte) []byte { binary.LittleEndian.PutUint64(b, 0); return b },
		func(b []byte) []byte { binary.LittleEndian.PutUint64(b, ^uint64(0)); return b },
		func(b []byte) []byte { binary.LittleEndian.PutUint32(b[8:], 0x10200); return b },
		func(b []byte) []byte { binary.LittleEndian.PutUint32(b[8:], 0x10400); return b },
		func(b []byte) []byte { binary.LittleEndian.PutUint32(b[8:], 0x20000); return b },
		func(b []byte) []byte { binary.LittleEndian.PutUint32(b[0x14:], 0); return b },
		func(b []byte) []byte { binary.LittleEndian.PutUint32(b[0x1c:], 0); return b },
		func(b []byte) []byte { binary.LittleEndian.PutUint32(b[0x1c:], 3); return b },
		func(b []byte) []byte { binary.LittleEndian.PutUint32(b[0x1c:], 5); return b },
		func(b []byte) []byte { binary.LittleEndian.PutUint32(b[0x1c:], 0x21); return b },
	} {
		if s, err := ReadNVMeOptional(edit(optionalBaseline(false)), func(int) (uint32, error) { t.Fatal("invalid baseline reached MMIO"); return 0, nil }); err == nil || s != nil {
			t.Fatal("invalid baseline accepted")
		}
	}
	if _, err := ReadNVMeOptional(optionalBaseline(false), nil); err == nil {
		t.Fatal("nil callback accepted")
	}
	for _, boot := range []bool{false, true} {
		extent, err := OptionalExtent(optionalBaseline(boot))
		want := 0x40
		if boot {
			want = 0x44
		}
		if err != nil || extent != want {
			t.Fatalf("extent=%#x err=%v", extent, err)
		}
	}
}

func TestOptionalInvalidCMBSZNeverReadsLocation(t *testing.T) {
	for _, sz := range []uint32{0xffffffff, 0x1701, 0x1021, 0x1, 0x1004} {
		count := 0
		s, err := ReadNVMeOptional(optionalBaseline(true), func(off int) (uint32, error) {
			count++
			if off != 0x3c {
				t.Fatal("invalid CMBSZ allowed another read")
			}
			return sz, nil
		})
		if err == nil || s != nil || count != 1 {
			t.Fatalf("CMBSZ=%#x snapshot=%v err=%v count=%d", sz, s, err, count)
		}
	}
}

func TestOptionalReadErrorsAndAllOnesStop(t *testing.T) {
	failure := errors.New("completion failure")
	for _, failAt := range []int{0x3c, 0x38, 0x40} {
		for _, allOnes := range []bool{false, true} {
			var reads []int
			s, err := ReadNVMeOptional(optionalBaseline(true), func(off int) (uint32, error) {
				reads = append(reads, off)
				if off == failAt {
					if allOnes {
						return ^uint32(0), nil
					}
					return 0, failure
				}
				if off == 0x3c {
					return 0x1001, nil
				}
				return 0, nil
			})
			if err == nil || s != nil || reads[len(reads)-1] != failAt {
				t.Fatalf("did not stop: %v %v", reads, err)
			}
			if !allOnes && !errors.Is(err, failure) {
				t.Fatal("lost backend error")
			}
		}
	}
}

func TestOptionalInvalidLocationAndBootMetadata(t *testing.T) {
	for _, loc := range []uint32{1, 6, 7, 8} {
		reads := 0
		s, err := ReadNVMeOptional(optionalBaseline(true), func(off int) (uint32, error) {
			reads++
			if off == 0x3c {
				return 0x1001, nil
			}
			return loc, nil
		})
		if err == nil || s != nil || reads != 2 {
			t.Fatal("invalid CMBLOC accepted or boot read continued")
		}
	}
	if s, err := ReadNVMeOptional(optionalBaseline(true), func(off int) (uint32, error) {
		if off == 0x40 {
			return 0x04000000, nil
		}
		return 0, nil
	}); err == nil || s != nil {
		t.Fatal("reserved BPINFO bits accepted")
	}
}

func TestOptionalLargeCMBIsOnlyMetadata(t *testing.T) {
	reads := 0
	s, err := ReadNVMeOptional(optionalBaseline(false), func(off int) (uint32, error) {
		reads++
		if off == 0x3c {
			return 0xfffff601, nil
		}
		return 0xfffff000, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if reads != 2 || s.CMB.Size != uint64(0xfffff)<<36 || s.CMB.Offset != s.CMB.Size {
		t.Fatal("large descriptor truncated or followed as memory")
	}
	for _, skipped := range s.Skipped {
		if strings.Contains(skipped.Name, "CMB memory") {
			return
		}
	}
	t.Fatal("CMB memory exclusion not reported")
}
