package githubpoll

import "os"

// stdGetenv wraps os.Getenv so it can be referenced as a value (and
// swapped in tests). Kept in its own file so other build files cannot
// accidentally rely on os.Getenv via package-level imports — they go
// through this indirection.
func stdGetenv(name string) string { return os.Getenv(name) }
