//go:build windows

package executor

import (
	"fmt"
	"os/exec"
	"strconv"
)

// killProcessTree 终止 cmd.exe 以及由它拉起的整棵子进程树。
//
// 为什么需要它：exec.CommandContext 默认只 Kill 直接子进程 cmd.exe。
// 一个 bat 里如果再启动 robocopy、7z、python 等，超时后这些孙进程会变成
// 孤儿继续占用文件和网络，下一次调度又会撞上它们。taskkill /T /F 按 PID
// 递归终止，能保证超时后任务真的停下来。
//
// 该函数挂在 exec.Cmd.Cancel 上，由 CommandContext 在 ctx 结束时自动调用；
// 返回 nil 让 Wait 保留 context.DeadlineExceeded 语义。
func killProcessTree(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}

	kill := exec.Command("taskkill", "/PID", strconv.Itoa(cmd.Process.Pid), "/T", "/F")
	setSysProcAttr(kill) // taskkill 自己也不能弹窗口
	out, err := kill.CombinedOutput()
	if err != nil {
		// taskkill 可能因进程已退出而失败，兜底再直接 Kill 一次主进程
		if kerr := cmd.Process.Kill(); kerr != nil {
			return fmt.Errorf("终止进程树失败: taskkill=%v (%s), Kill=%v", err, string(out), kerr)
		}
	}
	return nil
}
