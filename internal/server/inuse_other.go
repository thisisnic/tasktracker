//go:build !windows

package server

import (
	"errors"
	"syscall"
)

// inUse reports whether a listen failed because the address is taken.
func inUse(err error) bool { return errors.Is(err, syscall.EADDRINUSE) }
