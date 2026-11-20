//go:build !windows

package backend

import (
	"os"
	"os/exec"
)

// passMedia hands w to captured as fd 3, its default --media.
func passMedia(cmd *exec.Cmd, w *os.File) {
	cmd.ExtraFiles = []*os.File{w}
}
