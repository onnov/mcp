//go:build unix

package files

import "syscall"

const nonblock = syscall.O_NONBLOCK
