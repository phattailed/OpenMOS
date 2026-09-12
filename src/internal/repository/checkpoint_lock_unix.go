//go:build unix

package repository

import (
	"os"
	"syscall"
)

func lockCheckpoint(file *os.File) error {
	return syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}
