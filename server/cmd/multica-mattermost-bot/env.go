package main

import "os"

// Thin wrappers so config_test.go can stub env in a single place without
// dragging os/exec or build tags into tests. The defaults route through
// os directly; nothing fancy.
var (
	osGetenv   = os.Getenv
	osSetenv   = func(k, v string) { _ = os.Setenv(k, v) }
	osUnsetenv = func(k string) { _ = os.Unsetenv(k) }
)
