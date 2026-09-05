package main

import (
	"strings"
	"testing"
)

func TestNVMeNativeFlagDefaultsOffAndRejectsOffline(t *testing.T) {
	f := buildCmd.Flags().Lookup("nvme-native-snapshot")
	if f == nil || f.DefValue != "false" {
		t.Fatal("native admin reads enabled by default")
	}
	previous := buildOpts
	t.Cleanup(func() { buildOpts = previous })
	buildOpts = buildFlags{fromJSON: "must-not-be-opened.json", nvmeAdminSnapshot: true}
	if _, err := loadDonorContext(); err == nil || !strings.Contains(err.Error(), "requires live") {
		t.Fatalf("offline request not rejected before IO: %v", err)
	}
}
