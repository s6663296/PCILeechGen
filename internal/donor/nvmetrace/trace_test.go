package nvmetrace

import (
	"encoding/binary"
	"os"
	"strings"
	"testing"
)

const fixture = `# entries-in-buffer/entries-written: 3/3
 test-1 [000] ..... 10.000001: nvme_setup_cmd: nvme0: qid=0, cmdid=9, nsid=0, flags=0x0, meta=0x0, cmd=(nvme_admin_identify cns=1, ctrlid=0)
 test-1 [000] ..... 10.000020: nvme_sq: nvme0: qid=0, head=20, tail=21
 test-1 [000] ..... 10.000030: nvme_complete_rq: nvme0: qid=0, cmdid=9, res=0x0, retries=0, flags=0x2, status=0x0
`

func baseline(dstrd uint64) []byte {
	b := make([]byte, 56)
	binary.LittleEndian.PutUint64(b, 1|dstrd<<32)
	return b
}
func TestEvidenceAndStride(t *testing.T) {
	e, err := Parse([]byte(fixture), baseline(2), 0x4000, "nvme0", "0000:01:00.0")
	if err != nil {
		t.Fatal(err)
	}
	if e.MMIOObserved || e.Stride != 16 || e.Queues[0].CQOffset != 0x1010 || *e.Commands[0].CompleteUS-e.Commands[0].SetupUS != 29 {
		t.Fatalf("incorrect inference: %+v", e)
	}
	if e.Queues[0].Samples[0].Head == e.Queues[0].Samples[0].Tail {
		t.Fatal("lost distinct SQ head/tail")
	}
}
func TestRejectLossAndMalformed(t *testing.T) {
	for _, s := range []string{strings.Replace(fixture, "3/3", "2/3", 1), strings.Replace(fixture, "cmdid=9", "cmdid=999999", 1), strings.Replace(fixture, "10.000030", "9.000030", 1), fixture + "LOST EVENTS\n"} {
		if _, err := Parse([]byte(s), baseline(0), 0x4000, "nvme0", "x"); err == nil {
			t.Fatal("malformed capture accepted")
		}
	}
	if _, err := Parse([]byte(fixture), baseline(0), 0x1004, "nvme0", "x"); err == nil {
		t.Fatal("out-of-BAR CQ accepted")
	}
	if _, err := Parse([]byte(fixture), baseline(0), 0x4000, "nvme1", "x"); err == nil {
		t.Fatal("wrong controller accepted")
	}
}
func TestFailedAndUnmatchedCommandsRemainEvidence(t *testing.T) {
	s := strings.Replace(fixture, "status=0x0", "status=0x2", 1)
	e, err := Parse([]byte(s), baseline(0), 0x4000, "nvme0", "x")
	if err != nil || *e.Commands[0].Status != 2 {
		t.Fatalf("failure hidden: %v", err)
	}
	s = strings.Replace(fixture, "nvme_complete_rq: nvme0: qid=0, cmdid=9", "nvme_complete_rq: nvme0: qid=0, cmdid=10", 1)
	e, err = Parse([]byte(s), baseline(0), 0x4000, "nvme0", "x")
	if err != nil || e.UnfinishedCommands != 1 || e.UnmatchedCompletions != 1 {
		t.Fatalf("unmatched events hidden: %v", err)
	}
}

// Private donor trace is never committed. Set this variable for a local replay.
func TestPrivateCapture(t *testing.T) {
	path := os.Getenv("PCILEECHGEN_TRACE_TEST_FILE")
	if path == "" {
		t.Skip("no private capture")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	e, err := Parse(b, baseline(0), 0x4000, "nvme0", "0000:01:00.0")
	if err != nil {
		t.Fatal(err)
	}
	if len(e.Commands) != 10 || len(e.Queues) != 1 || len(e.Queues[0].Samples) != 10 || e.UnfinishedCommands != 0 || e.UnmatchedCompletions != 0 {
		t.Fatalf("capture mismatch: %+v", e)
	}
	for _, c := range e.Commands {
		if c.Status == nil || *c.Status != 0 || c.Retries == nil || *c.Retries != 0 {
			t.Fatal("capture has failures")
		}
	}
}
