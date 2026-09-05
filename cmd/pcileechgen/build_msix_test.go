package main

import (
	"strings"
	"testing"
)

func TestNVMeMSIXFlagDefaultsOff(t *testing.T) {
	flag := buildCmd.Flags().Lookup("nvme-msix-snapshot")
	if flag == nil || flag.DefValue != "off" || flag.NoOptDefVal != "" {
		t.Fatalf("missing flag or unsafe default: %+v", flag)
	}
}

func TestNVMeMSIXFlagRejectsOfflineAndInvalidMode(t *testing.T) {
	previous := buildOpts
	t.Cleanup(func() { buildOpts = previous })
	for _, mode := range []string{"table", "pba", "all"} {
		buildOpts = buildFlags{fromJSON: "must-not-be-opened.json", nvmeMSIXSnapshot: mode}
		if _, err := loadDonorContext(); err == nil || !strings.Contains(err.Error(), "requires live") {
			t.Fatalf("offline mode %s not rejected before file access: %v", mode, err)
		}
	}
	buildOpts = buildFlags{bdf: "0000:01:00.0", nvmeMSIXSnapshot: "full"}
	if _, err := loadDonorContext(); err == nil || !strings.Contains(err.Error(), "must be off") {
		t.Fatalf("invalid mode not rejected before device access: %v", err)
	}
}
