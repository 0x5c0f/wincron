//go:build windows

package executor

import (
	"os/exec"
	"syscall"
)

const (
	createNoWindow        = 0x08000000
	createNewProcessGroup = 0x00000200
)

// setSysProcAttr 在Windows上设置进程属性，隐藏窗口。
// CREATE_NO_WINDOW = 0x08000000 确保不创建控制台窗口
// CREATE_NEW_PROCESS_GROUP = 0x00000200 创建新进程组，便于整组控制
// 这是在 Session 0 中后台执行任务的关键设置：
// cmd /c 拉起的子进程继承服务的会话，不弹窗口、不需要用户登录桌面。
func setSysProcAttr(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.HideWindow = true
	cmd.SysProcAttr.CreationFlags = createNoWindow | createNewProcessGroup
}
