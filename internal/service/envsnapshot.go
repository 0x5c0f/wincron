package service

import (
	"fmt"
	"os"
	"path/filepath"

	"wincron/internal/doctor"
	"wincron/internal/paths"
)

// writeEnvSnapshot 把服务进程的环境快照写入日志文件。
// 每次服务启动覆盖一次，方便对比"服务账户变更前/后"的差异。
// logDir 是相对路径时必须以 exe 目录为基准，否则服务会把快照写进 System32。
func writeEnvSnapshot(info doctor.EnvInfo, logDir string) {
	absDir := paths.Resolve(logDir)
	if err := os.MkdirAll(absDir, 0755); err != nil {
		fmt.Printf("[WinCron] 创建日志目录失败 (%s): %v\n", absDir, err)
		return
	}
	path := filepath.Join(absDir, "environment.log")
	content := fmt.Sprintf(" WinCron 服务进程环境快照 \n"+
		" 生成时机: 服务启动时 (每次启动覆盖)\n"+
		" 用途: 任务脚本的实际运行环境，区别于管理员终端环境\n\n") + info.Report()
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		fmt.Printf("[WinCron] 写入环境快照失败 (%s): %v\n", path, err)
		return
	}
	fmt.Printf("[WinCron] 环境快照已写入: %s\n", path)
}
