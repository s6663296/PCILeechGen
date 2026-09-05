package main

import "testing"

func TestTraceRequiresOfflineAndExplicitAssociation(t *testing.T) {
	old := buildOpts
	defer func() { buildOpts = old }()
	for _, flags := range []buildFlags{{nvmeTrace: "x"}, {nvmeTraceController: "nvme0"}, {nvmeTrace: "x", nvmeTraceController: "nvme0"}, {fromJSON: "missing", nvmeTrace: "x"}} {
		buildOpts = flags
		if _, err := loadDonorContext(); err == nil {
			t.Fatal("incomplete trace flags accepted")
		}
	}
}
