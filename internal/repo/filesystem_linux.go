package repo

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

func renameNoReplace(from, to string) error {
	if err := unix.Renameat2(unix.AT_FDCWD, from, unix.AT_FDCWD, to, unix.RENAME_NOREPLACE); err != nil {
		return fmt.Errorf("moving %s to %s without overwriting: %w", from, to, err)
	}
	return nil
}

func lockCheckout(dir string) (func(), error) {
	f, err := os.OpenFile(filepath.Join(dir, ".git", "tugboat.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, err
	}
	if err := cleanupGitConfigLock(dir); err != nil {
		_ = f.Close()
		return nil, err
	}
	return func() { _ = unix.Flock(int(f.Fd()), unix.LOCK_UN); _ = f.Close() }, nil
}
