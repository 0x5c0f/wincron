package executor

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newLogTestExecutor(t *testing.T) *Executor {
	t.Helper()
	e := NewExecutor(t.TempDir())
	t.Cleanup(e.Close)
	return e
}

func fillLogs(e *Executor, n int) {
	for i := 0; i < n; i++ {
		jobID := "alpha"
		if i%2 == 0 {
			jobID = "beta"
		}
		e.logEntries = append(e.logEntries, LogEntry{
			ID:      fmt.Sprintf("entry-%02d", i),
			JobID:   jobID,
			JobName: jobID,
			Time:    time.Now().Add(time.Duration(i) * time.Second),
		})
	}
}

// 分页必须最新在前，且 total 是匹配总数而不是本页条数
func TestGetLogsPageReturnsNewestFirstWithTotal(t *testing.T) {
	e := newLogTestExecutor(t)
	fillLogs(e, 25)

	page, total := e.GetLogsPage("", 0, 10)
	if total != 25 || len(page) != 10 {
		t.Fatalf("第一页异常: total=%d len=%d", total, len(page))
	}
	if page[0].ID != "entry-24" || page[9].ID != "entry-15" {
		t.Errorf("第一页顺序/范围错误: first=%s last=%s", page[0].ID, page[9].ID)
	}

	page, total = e.GetLogsPage("", 10, 10)
	if total != 25 || len(page) != 10 || page[0].ID != "entry-14" {
		t.Errorf("第二页异常: total=%d len=%d first=%s", total, len(page), page[0].ID)
	}

	page, total = e.GetLogsPage("", 20, 10)
	if total != 25 || len(page) != 5 || page[0].ID != "entry-04" || page[4].ID != "entry-00" {
		t.Errorf("末页异常: total=%d len=%d first=%s last=%s", total, len(page), page[0].ID, page[len(page)-1].ID)
	}
}

// 按任务筛选时 total 只统计该任务的日志
func TestGetLogsPageFiltersByJob(t *testing.T) {
	e := newLogTestExecutor(t)
	fillLogs(e, 25)

	page, total := e.GetLogsPage("alpha", 0, 100)
	if total != 12 || len(page) != 12 {
		t.Fatalf("alpha 应有 12 条（奇数索引），实际 total=%d len=%d", total, len(page))
	}
	for _, l := range page {
		if l.JobID != "alpha" {
			t.Fatalf("筛选结果混入其它任务: %+v", l)
		}
	}
	if _, total := e.GetLogsPage("missing-job", 0, 10); total != 0 {
		t.Errorf("不存在任务的总数应为 0，实际 %d", total)
	}
}

// 越界/非法分页参数必须夹紧，不能 panic 也不能返回负数切片
func TestGetLogsPageClampsBadArguments(t *testing.T) {
	e := newLogTestExecutor(t)
	fillLogs(e, 25)

	for _, tc := range []struct {
		name      string
		offset    int
		limit     int
		wantLen   int
		wantFirst string
	}{
		{"offset 超出总数", 999, 10, 0, ""},
		{"offset 为负", -5, 3, 3, "entry-24"},
		{"limit 为 0 回退 20", 0, 0, 20, "entry-24"},
		{"limit 为负回退 20", 0, -1, 20, "entry-24"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			page, total := e.GetLogsPage("", tc.offset, tc.limit)
			if total != 25 || len(page) != tc.wantLen {
				t.Fatalf("total=%d len=%d, want len=%d", total, len(page), tc.wantLen)
			}
			if tc.wantFirst != "" && page[0].ID != tc.wantFirst {
				t.Errorf("首条应为 %s，实际 %s", tc.wantFirst, page[0].ID)
			}
		})
	}
}

// 调小日志上限时内存里已有的历史条目要立即裁剪
func TestSetLimitsClampsAndTrimsExistingEntries(t *testing.T) {
	e := newLogTestExecutor(t)
	fillLogs(e, 50)

	e.SetLimits(10, 4096)
	if len(e.logEntries) != 10 {
		t.Fatalf("调小上限后应保留 10 条，实际 %d", len(e.logEntries))
	}
	if e.logEntries[0].ID != "entry-40" || e.logEntries[9].ID != "entry-49" {
		t.Errorf("应保留最近 10 条: %s ~ %s", e.logEntries[0].ID, e.logEntries[9].ID)
	}
	_, total := e.GetLogsPage("", 0, 100)
	if total != 10 {
		t.Errorf("分页总数应随之变为 10，实际 %d", total)
	}

	e.SetLimits(1, 16)
	if e.maxLogCount != 10 {
		t.Errorf("maxLogCount 下限应夹到 10，实际 %d", e.maxLogCount)
	}
	if e.maxFileBytes != 1024 {
		t.Errorf("maxFileBytes 下限应夹到 1024，实际 %d", e.maxFileBytes)
	}
	e.SetLimits(200000, 1<<40)
	if e.maxLogCount != 100000 {
		t.Errorf("maxLogCount 上限应夹到 100000，实际 %d", e.maxLogCount)
	}
}

// addLog 是真实写入路径：既要限制内存条目，也要限制 .log 文件体积
func TestAddLogRespectsBothLimits(t *testing.T) {
	e := newLogTestExecutor(t)
	e.SetLimits(10, 4096)

	for i := 0; i < 30; i++ {
		e.addLog(&ExecResult{
			JobID:     "alpha",
			JobName:   "alpha",
			StartTime: time.Now().Add(time.Duration(i) * time.Millisecond),
			Output:    strings.Repeat("x", 300),
			Success:   true,
		})
	}

	if len(e.logEntries) != 10 {
		t.Errorf("内存中应只保留 10 条，实际 %d", len(e.logEntries))
	}
	_, total := e.GetLogsPage("", 0, 100)
	if total != 10 {
		t.Errorf("分页总数应为 10，实际 %d", total)
	}

	fi, err := os.Stat(filepath.Join(e.LogDir(), "alpha.log"))
	if err != nil {
		t.Fatalf("日志文件未生成: %v", err)
	}
	// 裁剪后最多保留半份 + 单条记录，给一点余量
	if fi.Size() > 4096+1024 {
		t.Errorf("日志文件超出上限仍在增长: %d 字节", fi.Size())
	}
}

// 文件超限时保留最近内容并写入裁剪说明；未超限则一个字节都不动
func TestTrimLogFileKeepsRecentAndBoundsSize(t *testing.T) {
	e := newLogTestExecutor(t)
	e.SetLimits(500, 4096)
	logFile := filepath.Join(e.LogDir(), "alpha.log")
	if err := os.MkdirAll(e.LogDir(), 0755); err != nil {
		t.Fatal(err)
	}

	var sb strings.Builder
	for i := 0; i < 20; i++ {
		fmt.Fprintf(&sb, "line-%02d %s\n", i, strings.Repeat("x", 280))
	}
	original := sb.String()
	if err := os.WriteFile(logFile, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}

	// 上限设为很大时不应裁剪
	e.SetLimits(500, 10<<20)
	e.trimLogFile(logFile, 100)
	unchanged, _ := os.ReadFile(logFile)
	if string(unchanged) != original {
		t.Fatal("未超过上限却被裁剪了")
	}

	e.SetLimits(500, 4096)
	e.trimLogFile(logFile, 100)
	data, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatal(err)
	}
	preview := string(data)
	if len(preview) > 80 {
		preview = preview[:80]
	}
	if !strings.HasPrefix(string(data), "[WinCron] 日志已达大小上限") {
		t.Errorf("裁剪后缺少说明头: %q", preview)
	}
	if int64(len(data)) > 4096 {
		t.Errorf("裁剪后仍超过上限: %d 字节", len(data))
	}
	if !strings.Contains(string(data), "line-19") {
		t.Error("最近记录被错误裁掉")
	}
	if strings.Contains(string(data), "line-00") {
		t.Error("最早记录仍保留，说明裁剪没有生效")
	}
}
