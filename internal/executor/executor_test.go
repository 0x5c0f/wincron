package executor

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"golang.org/x/text/encoding/simplifiedchinese"

	"wincron/internal/config"
)

func testJob(command string, timeout int) *config.JobConfig {
	return &config.JobConfig{
		ID:       "test-job",
		Name:     "测试任务",
		Command:  command,
		Enabled:  true,
		Timeout:  timeout,
		MaxRetry: 0,
		Env:      map[string]string{},
	}
}

func TestExecuteCapturesStdout(t *testing.T) {
	e := NewExecutor(t.TempDir())
	defer e.Close()

	r := e.Execute(testJob("echo WinCron_E2E_OK", 0))
	if !r.Success {
		t.Fatalf("命令执行失败: %+v", r)
	}
	if !strings.Contains(r.Output, "WinCron_E2E_OK") {
		t.Errorf("未捕获到标准输出: %q", r.Output)
	}
	if len(e.GetAllLogs(10)) != 1 {
		t.Error("执行结果未写入内存日志")
	}
}

// 验证超时后能及时返回，且不会只杀 cmd.exe 把孙进程留成孤儿。
// 命令本身要跑约 20 秒，超时设为 1 秒。
func TestExecuteTimeoutTerminates(t *testing.T) {
	cmd := "sleep 20"
	if runtime.GOOS == "windows" {
		cmd = "ping -n 20 127.0.0.1 >nul"
	}

	e := NewExecutor(t.TempDir())
	defer e.Close()

	start := time.Now()
	r := e.Execute(testJob(cmd, 1))
	elapsed := time.Since(start)

	if r.Success {
		t.Error("超时任务不应被判定为成功")
	}
	if elapsed > 12*time.Second {
		t.Errorf("超时后未在合理时间内返回: %v", elapsed)
	}
	if !strings.Contains(r.Error, "超时终止") {
		t.Errorf("错误信息缺少超时说明: %q", r.Error)
	}
}

func TestDecodeOutput(t *testing.T) {
	encoder := simplifiedchinese.GBK.NewEncoder()
	gbk, err := encoder.Bytes([]byte("中文测试"))
	if err != nil {
		t.Fatal(err)
	}
	if got := decodeOutput(gbk); got != "中文测试" {
		t.Errorf("GBK 解码失败: %q", got)
	}
	if got := decodeOutput([]byte("plain ascii")); got != "plain ascii" {
		t.Errorf("ASCII/UTF-8 不应被改动: %q", got)
	}
	if got := decodeOutput(nil); got != "" {
		t.Errorf("空输入应返回空: %q", got)
	}
}

func TestCleanOldLogsKeepsEnvironmentSnapshot(t *testing.T) {
	dir := t.TempDir()
	e := NewExecutor(dir)
	defer e.Close()

	old := time.Now().AddDate(0, 0, -60)
	for _, name := range []string{"job-1.log", "environment.log"} {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatal(err)
		}
	}

	removed := e.CleanOldLogs(30)
	if removed != 1 {
		t.Errorf("应只清理 1 个过期任务日志，实际 %d", removed)
	}
	if _, err := os.Stat(filepath.Join(dir, "environment.log")); err != nil {
		t.Error("environment.log 不应被清理")
	}
}
