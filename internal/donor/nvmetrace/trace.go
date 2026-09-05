// Package nvmetrace imports text tracepoint evidence without accessing hardware.
package nvmetrace

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const MaxBytes = 8 << 20

type Evidence struct {
	Policy               string    `json:"policy"`
	SHA256               string    `json:"source_sha256"`
	Controller           string    `json:"controller"`
	BDF                  string    `json:"user_associated_bdf"`
	Association          string    `json:"association"`
	DSTRD                uint8     `json:"dstrd"`
	Stride               uint64    `json:"doorbell_stride_bytes"`
	MMIOObserved         bool      `json:"mmio_observed"`
	EntriesInBuffer      uint64    `json:"entries_in_buffer"`
	EntriesWritten       uint64    `json:"entries_written"`
	Commands             []Command `json:"commands"`
	Queues               []Queue   `json:"observed_queues"`
	UnmatchedCompletions int       `json:"unmatched_completions"`
	UnfinishedCommands   int       `json:"unfinished_commands"`
	AsyncEvents          int       `json:"async_events"`
	IgnoredEvents        int       `json:"other_controller_events"`
	Limitations          []string  `json:"limitations"`
}

type Command struct {
	QID         uint16  `json:"qid"`
	CID         uint16  `json:"cid"`
	NSID        uint32  `json:"nsid"`
	Description string  `json:"description"`
	SetupUS     uint64  `json:"setup_timestamp_us"`
	CompleteUS  *uint64 `json:"completion_timestamp_us,omitempty"`
	Status      *uint16 `json:"status,omitempty"`
	Retries     *uint32 `json:"retries,omitempty"`
}

type Queue struct {
	QID      uint16   `json:"qid"`
	SQOffset uint64   `json:"specified_sq_doorbell_offset"`
	CQOffset uint64   `json:"specified_cq_doorbell_offset"`
	Samples  []Sample `json:"sq_tracepoint_samples"`
}
type Sample struct {
	TimestampUS uint64 `json:"timestamp_us"`
	Head        uint16 `json:"head"`
	Tail        uint16 `json:"tail"`
}

var headerRE = regexp.MustCompile(`entries-in-buffer/entries-written:\s*(\d+)/(\d+)`)
var eventRE = regexp.MustCompile(`\s(\d+)\.(\d{6}): (nvme_[a-z_]+): (nvme[0-9]+): (.*)$`)
var controllerRE = regexp.MustCompile(`^nvme[0-9]+$`)
var setupRE = regexp.MustCompile(`^qid=(\d+), cmdid=(\d+), nsid=(\d+), flags=0x[0-9a-fA-F]+, meta=0x[0-9a-fA-F]+, cmd=\((.*)\)$`)
var sqRE = regexp.MustCompile(`^qid=(\d+), head=(\d+), tail=(\d+)$`)
var completeRE = regexp.MustCompile(`^qid=(\d+), cmdid=(\d+), res=0x[0-9a-fA-F]+, retries=(\d+), flags=0x[0-9a-fA-F]+, status=0x([0-9a-fA-F]+)$`)

// Parse accepts the reviewed six-decimal ftrace text format. Controller-to-BDF
// association is explicit user input: these tracepoints contain no PCI identity.
func Parse(data, baseline []byte, barSize uint64, controller, bdf string) (*Evidence, error) {
	if len(data) == 0 || len(data) > MaxBytes || !controllerRE.MatchString(controller) {
		return nil, fmt.Errorf("invalid trace size or controller name")
	}
	if len(baseline) < 8 {
		return nil, fmt.Errorf("CAP missing from baseline")
	}
	cap := binary.LittleEndian.Uint64(baseline)
	if cap == 0 || cap == ^uint64(0) {
		return nil, fmt.Errorf("invalid baseline CAP")
	}
	e := &Evidence{Policy: "nvme-tracepoint-evidence-v1", SHA256: fmt.Sprintf("%x", sha256.Sum256(data)), Controller: controller, BDF: bdf, Association: "user_asserted_not_verified_by_trace", DSTRD: uint8(cap >> 32 & 15), Limitations: []string{
		"Tracepoints are software observations, not MMIO or PCIe TLP measurements.",
		"SQ head/tail samples are not CQ head samples or measured doorbell writes.",
		"Offsets are specified from CAP.DSTRD and observed QIDs; unobserved queues are not inventoried.",
		"No reset values, queue depth, interrupts, DBBUF state or hardware latency are inferred.",
		"Trace and donor snapshot may be from different runs; controller-to-BDF association is user asserted.",
		"Buffer counters only describe this capture, not all possible sources of event loss.",
	}}
	e.Stride = uint64(4) << e.DSTRD
	queues := map[uint16]*Queue{}
	pending := map[[2]uint16]int{}
	header := false
	events := uint64(0)
	selected := 0
	number := func(s string, base, bits int) (uint64, error) { return strconv.ParseUint(s, base, bits) }
	queue := func(q uint16) (*Queue, error) {
		if v := queues[q]; v != nil {
			return v, nil
		}
		sq := uint64(0x1000) + 2*uint64(q)*e.Stride
		cq := sq + e.Stride
		if cq+4 > barSize {
			return nil, fmt.Errorf("QID %d doorbell exceeds saved BAR0 extent", q)
		}
		v := &Queue{QID: q, SQOffset: sq, CQOffset: cq}
		queues[q] = v
		return v, nil
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 4096), 65536)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#") {
			if m := headerRE.FindStringSubmatch(line); m != nil {
				if header {
					return nil, fmt.Errorf("multiple trace headers")
				}
				header = true
				var err error
				e.EntriesInBuffer, err = number(m[1], 10, 64)
				if err != nil {
					return nil, err
				}
				e.EntriesWritten, err = number(m[2], 10, 64)
				if err != nil {
					return nil, err
				}
			}
			continue
		}
		m := eventRE.FindStringSubmatch(" " + line)
		if m == nil {
			return nil, fmt.Errorf("unsupported or lost-event trace line: %.160s", line)
		}
		events++
		if m[4] != controller {
			e.IgnoredEvents++
			continue
		}
		selected++
		secs, err := number(m[1], 10, 64)
		if err != nil || secs > (^uint64(0)-999999)/1000000 {
			return nil, fmt.Errorf("invalid timestamp")
		}
		frac, _ := number(m[2], 10, 32)
		ts := secs*1000000 + frac
		switch m[3] {
		case "nvme_setup_cmd":
			p := setupRE.FindStringSubmatch(m[5])
			if p == nil {
				return nil, fmt.Errorf("unsupported setup format")
			}
			q, err := number(p[1], 10, 16)
			if err != nil {
				return nil, err
			}
			cid, err := number(p[2], 10, 16)
			if err != nil {
				return nil, err
			}
			ns, err := number(p[3], 10, 32)
			if err != nil {
				return nil, err
			}
			if _, err = queue(uint16(q)); err != nil {
				return nil, err
			}
			key := [2]uint16{uint16(q), uint16(cid)}
			if _, ok := pending[key]; ok {
				return nil, fmt.Errorf("duplicate in-flight QID/CID")
			}
			pending[key] = len(e.Commands)
			e.Commands = append(e.Commands, Command{QID: uint16(q), CID: uint16(cid), NSID: uint32(ns), Description: p[4], SetupUS: ts})
		case "nvme_sq":
			p := sqRE.FindStringSubmatch(m[5])
			if p == nil {
				return nil, fmt.Errorf("unsupported SQ format")
			}
			q, err := number(p[1], 10, 16)
			if err != nil {
				return nil, err
			}
			h, err := number(p[2], 10, 16)
			if err != nil {
				return nil, err
			}
			t, err := number(p[3], 10, 16)
			if err != nil {
				return nil, err
			}
			v, err := queue(uint16(q))
			if err != nil {
				return nil, err
			}
			v.Samples = append(v.Samples, Sample{TimestampUS: ts, Head: uint16(h), Tail: uint16(t)})
		case "nvme_complete_rq":
			p := completeRE.FindStringSubmatch(m[5])
			if p == nil {
				return nil, fmt.Errorf("unsupported completion format")
			}
			q, err := number(p[1], 10, 16)
			if err != nil {
				return nil, err
			}
			cid, err := number(p[2], 10, 16)
			if err != nil {
				return nil, err
			}
			retry, err := number(p[3], 10, 32)
			if err != nil {
				return nil, err
			}
			status, err := number(p[4], 16, 16)
			if err != nil {
				return nil, err
			}
			if _, err = queue(uint16(q)); err != nil {
				return nil, err
			}
			key := [2]uint16{uint16(q), uint16(cid)}
			i, ok := pending[key]
			if !ok {
				e.UnmatchedCompletions++
				continue
			}
			c := &e.Commands[i]
			if ts < c.SetupUS {
				return nil, fmt.Errorf("completion precedes setup; check trace clock")
			}
			s, r := uint16(status), uint32(retry)
			c.CompleteUS = &ts
			c.Status = &s
			c.Retries = &r
			delete(pending, key)
		case "nvme_async_event":
			e.AsyncEvents++
		default:
			return nil, fmt.Errorf("unsupported event %s", m[3])
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if !header || e.EntriesInBuffer != events || e.EntriesWritten != events {
		return nil, fmt.Errorf("missing header, truncated trace or buffer overwrite: parsed=%d buffered=%d written=%d", events, e.EntriesInBuffer, e.EntriesWritten)
	}
	if selected == 0 || len(e.Commands) == 0 {
		return nil, fmt.Errorf("no command evidence for %s", controller)
	}
	e.UnfinishedCommands = len(pending)
	for _, q := range queues {
		e.Queues = append(e.Queues, *q)
	}
	sort.Slice(e.Queues, func(i, j int) bool { return e.Queues[i].QID < e.Queues[j].QID })
	return e, nil
}
