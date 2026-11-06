//go:build !windows

package executor

import (
	"os/exec"
	"syscall"
)

// setSysProcAttr 非 Windows 平台：独立进程组，便于超时时整组终止
func setSysProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}
