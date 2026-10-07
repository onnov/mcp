//go:build linux

package config

import "syscall"

// A project file changing into a FIFO must not block server startup.
const scanNonblock = syscall.O_NONBLOCK
