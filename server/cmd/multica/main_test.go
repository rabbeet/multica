package main

import "testing"

// TestDefaultUnldflaggedVersionMatchesGateWhitelist locks the bare-"dev"
// signal that server/pkg/agent/version.go (CheckMinCLIVersion) whitelists to
// the actual default in this package. Importing across the cmd→lib boundary
// to share a constant would invert the dependency direction, so this test is
// the cheapest tripwire — if anyone renames the default to e.g. "unset" /
// "local" / "" the gate silently breaks again and CI fails here with a clear
// pointer to the whitelist site.
func TestDefaultUnldflaggedVersionMatchesGateWhitelist(t *testing.T) {
	if version != "dev" {
		t.Fatalf("default version is %q, but server/pkg/agent/version.go (CheckMinCLIVersion) whitelists exactly \"dev\" — update either the default or the whitelist", version)
	}
}
