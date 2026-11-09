package feeds

import (
	"os/exec"
	"syscall"
)

// Child marks cmd so the kernel kills it if camwall dies first, even when
// camwall is killed hard and cannot stop its FFmpeg processes itself.
func Child(cmd *exec.Cmd) *exec.Cmd {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Pdeathsig = syscall.SIGKILL
	return cmd
}
