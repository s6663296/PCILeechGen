package baraccess

import (
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/sercanarga/pcileechgen/internal/pci"
)

func msixPlanFixture() ([]pci.BAR, *pci.MSIXInfo) {
	return []pci.BAR{{Index: 0, Type: pci.BARTypeMem64, Size: 65536}},
		&pci.MSIXInfo{TableSize: 16, TableBIR: 0, TableOffset: 0x2000, PBABIR: 0, PBAOffset: 0x2100}
}

func TestNVMeMSIXModesAndExactReads(t *testing.T) {
	for _, mode := range []MSIXSnapshotMode{MSIXSnapshotTable, MSIXSnapshotPBA, MSIXSnapshotAll} {
		t.Run(string(mode), func(t *testing.T) {
			bars, info := msixPlanFixture()
			plan, err := PlanNVMeMSIX(bars, info, mode)
			if err != nil {
				t.Fatal(err)
			}
			var want, reads []uint64
			if mode != MSIXSnapshotPBA {
				for off := uint64(0x2000); off < 0x2100; off += 4 {
					want = append(want, off)
				}
			}
			if mode != MSIXSnapshotTable {
				want = append(want, 0x2100, 0x2104)
			}
			for _, r := range plan {
				data, err := ReadMSIXRange(r, func(off uint64) (uint32, error) {
					reads = append(reads, off)
					return uint32(off), nil
				})
				if err != nil || len(data) != r.Length {
					t.Fatalf("length=%d err=%v", len(data), err)
				}
			}
			if !reflect.DeepEqual(reads, want) {
				t.Fatalf("access list mismatch: got=%#v want=%#v", reads, want)
			}
		})
	}
}

func TestNVMeMSIXDefaultAndInvalidMode(t *testing.T) {
	for _, mode := range []MSIXSnapshotMode{"", MSIXSnapshotOff} {
		if plan, err := PlanNVMeMSIX(nil, nil, mode); err != nil || plan != nil {
			t.Fatalf("off mode accessed metadata: %v %v", plan, err)
		}
	}
	for _, mode := range []string{"on", "true", "TABLE", "0x2000", "full"} {
		if _, err := ParseMSIXSnapshotMode(mode); err == nil {
			t.Fatalf("accepted mode %q", mode)
		}
	}
}

func TestNVMeMSIXRejectsInvalidGeometry(t *testing.T) {
	cases := []struct {
		name string
		edit func([]pci.BAR, *pci.MSIXInfo)
	}{
		{"zero vectors", func(_ []pci.BAR, i *pci.MSIXInfo) { i.TableSize = 0 }},
		{"negative vectors", func(_ []pci.BAR, i *pci.MSIXInfo) { i.TableSize = -1 }},
		{"too many vectors", func(_ []pci.BAR, i *pci.MSIXInfo) { i.TableSize = 2049 }},
		{"other table BAR", func(_ []pci.BAR, i *pci.MSIXInfo) { i.TableBIR = 2 }},
		{"upper BAR", func(_ []pci.BAR, i *pci.MSIXInfo) { i.PBABIR = 1 }},
		{"bad BIR", func(_ []pci.BAR, i *pci.MSIXInfo) { i.PBABIR = 7 }},
		{"controller page", func(_ []pci.BAR, i *pci.MSIXInfo) { i.TableOffset = 0x800 }},
		{"doorbell page", func(_ []pci.BAR, i *pci.MSIXInfo) { i.TableOffset = 0x1000 }},
		{"unaligned", func(_ []pci.BAR, i *pci.MSIXInfo) { i.PBAOffset = 0x2104 }},
		{"overlap", func(_ []pci.BAR, i *pci.MSIXInfo) { i.PBAOffset = 0x20f8 }},
		{"PBA past end", func(b []pci.BAR, _ *pci.MSIXInfo) { b[0].Size = 0x2104 }},
		{"table past end", func(_ []pci.BAR, i *pci.MSIXInfo) { i.TableOffset = 0xfff8 }},
		{"32 bit end overflow", func(_ []pci.BAR, i *pci.MSIXInfo) { i.TableOffset = math.MaxUint32 - 7 }},
		{"IO BAR", func(b []pci.BAR, _ *pci.MSIXInfo) { b[0].Type = pci.BARTypeIO }},
		{"disabled BAR", func(b []pci.BAR, _ *pci.MSIXInfo) { b[0].Size = 0 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bars, info := msixPlanFixture()
			tc.edit(bars, info)
			for _, mode := range []MSIXSnapshotMode{MSIXSnapshotTable, MSIXSnapshotPBA, MSIXSnapshotAll} {
				if _, err := PlanNVMeMSIX(bars, info, mode); err == nil {
					t.Fatalf("invalid metadata accepted in mode %s", mode)
				}
			}
		})
	}
	bars, info := msixPlanFixture()
	if _, err := PlanNVMeMSIX(nil, info, MSIXSnapshotAll); err == nil {
		t.Fatal("missing BAR accepted")
	}
	if _, err := PlanNVMeMSIX(append(bars, bars[0]), info, MSIXSnapshotAll); err == nil {
		t.Fatal("duplicate BAR accepted")
	}
	if _, err := PlanNVMeMSIX(bars, nil, MSIXSnapshotAll); err == nil {
		t.Fatal("missing capability accepted")
	}
}

func TestNVMeMSIXVectorLengthsAndLargeGap(t *testing.T) {
	for _, vectors := range []int{1, 16, 64, 65, 2048} {
		bars, info := msixPlanFixture()
		info.TableSize, info.PBAOffset = vectors, 0xe000
		plan, err := PlanNVMeMSIX(bars, info, MSIXSnapshotAll)
		if err != nil || len(plan) != 2 {
			t.Fatalf("vectors=%d plan=%v err=%v", vectors, plan, err)
		}
		if plan[0].Length != vectors*16 || plan[1].Length != ((vectors+63)/64)*8 {
			t.Fatal("incorrect table/PBA extent")
		}
	}
	bars, info := msixPlanFixture()
	bars[0].Size = 0x20000000
	info.TableOffset, info.PBAOffset = 0x10000008, 0x18000000
	plan, err := PlanNVMeMSIX(bars, info, MSIXSnapshotAll)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, r := range plan {
		data, err := ReadMSIXRange(r, func(uint64) (uint32, error) { count++; return 0, nil })
		if err != nil || len(data) != r.Length {
			t.Fatal("failed compact discontiguous capture")
		}
	}
	if count != 66 {
		t.Fatalf("read across a gap: %d DWORDs", count)
	}
}

func TestNVMeMSIXReadFailureStopsImmediately(t *testing.T) {
	failure := errors.New("read failure")
	count := 0
	r := MSIXReadRange{Kind: "table", Offset: 0x2000, Length: 256}
	data, err := ReadMSIXRange(r, func(uint64) (uint32, error) {
		count++
		if count == 2 {
			return 0, failure
		}
		return 1, nil
	})
	if !errors.Is(err, failure) || !strings.Contains(err.Error(), "0x2004") || count != 2 || data != nil {
		t.Fatalf("data=%v err=%v reads=%d", data, err, count)
	}
	for _, bad := range []MSIXReadRange{
		{Kind: "table", Offset: 0x1000, Length: 16},
		{Kind: "table", Offset: 0x2000, Length: -1},
		{Kind: "table", Offset: 0x2000, Length: 65536},
		{Kind: "pba", Offset: 0x2000, Length: 264},
		{Kind: "other", Offset: 0x2000, Length: 16},
		{Kind: "table", Offset: math.MaxUint64 - 7, Length: 16},
	} {
		if _, err := ReadMSIXRange(bad, func(uint64) (uint32, error) {
			t.Fatal("invalid range reached reader")
			return 0, nil
		}); err == nil {
			t.Fatal("invalid range accepted")
		}
	}
}
