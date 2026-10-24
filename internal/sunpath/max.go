package sunpath

import "syscall"

const Max = len(syscall.RawSockaddrUnix{}.Path) - 1
