//go:build !windows

package executor

import (
	"os/exec"
	"syscall"
)

// killProcessTree 非 Windows 平台：向独立进程组发 SIGKILL。
// setSysProcAttr 已设置 Setpgid，因此 -pid 会命中整棵进程树。
func killProcessTree(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
		return cmd.Process.Kill()
	}
	return nil
}
