//go:build !unix

package main

import "errors"

// acquireLock falls back to "no lock" on non-unix platforms. The bot is
// Linux-only in production; this stub exists so go build cross-compiles
// for other targets during CI.
func acquireLock(path string) (func(), error) {
	return func() {}, errors.New("multica-mattermost-bot: startup lock requires a unix host")
}
