package server

import (
	"errors"
	"syscall"
)

// wsaEADDRINUSE is Winsock's "address already in use". The syscall
// package's EADDRINUSE on Windows is a placeholder that no socket call
// returns, so the number is spelled out, as golang.org/x/sys/windows
// spells it.
const wsaEADDRINUSE = syscall.Errno(10048)

// inUse reports whether a listen failed because the address is taken.
func inUse(err error) bool { return errors.Is(err, wsaEADDRINUSE) }
