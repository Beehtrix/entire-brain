//go:build !windows

package base

import "golang.org/x/sys/unix"

func MemoryStateReadOpenFlags() int { return unix.O_NONBLOCK }
