package backend

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

// passMedia makes w inheritable and tells captured its handle value, since
// Windows has no fd 3.
func passMedia(cmd *exec.Cmd, w *os.File) {
	h := syscall.Handle(w.Fd())
	syscall.SetHandleInformation(h, syscall.HANDLE_FLAG_INHERIT, syscall.HANDLE_FLAG_INHERIT)
	cmd.SysProcAttr = &syscall.SysProcAttr{AdditionalInheritedHandles: []syscall.Handle{h}}
	cmd.Args = append(cmd.Args, "--media", fmt.Sprintf("fd:%d", h))
}
