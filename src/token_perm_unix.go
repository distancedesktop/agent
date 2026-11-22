//go:build !windows

package main

import (
	"fmt"
	"os"
)

func restrictTokenFile(string) error { return nil }

func checkTokenFile(path string) error {
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("token file %s: mode %04o is readable by other users; chmod 600 it", path, fi.Mode().Perm())
	}
	return nil
}
