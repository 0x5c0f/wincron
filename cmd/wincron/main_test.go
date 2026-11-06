package main

import (
	"bufio"
	"reflect"
	"strings"
	"testing"

	"wincron/internal/config"
	"wincron/internal/cronexpr"
)

func TestReadLinePreservesSpacesAndHandlesCRLF(t *testing.T) {
	input := "* * * * *\r\n" + `echo aaa >> c:\b.txt` + "\r\n\n   \r\n"
	r := bufio.NewReader(strings.NewReader(input))

	if got := readLine(r, ""); got != "* * * * *" {
		t.Errorf("第 1 行应保留 5 个星号，实际 %q", got)
	}
	if got := readLine(r, ""); got != `echo aaa >> c:\b.txt` {
		t.Errorf("第 2 行应保留完整命令，实际 %q", got)
	}
	if got := readLine(r, ""); got != "" {
		t.Errorf("空行应返回空字符串，实际 %q", got)
	}
	if got := readLine(r, ""); got != "" {
		t.Errorf("纯空白行应返回空字符串，实际 %q", got)
	}
}

// 完整回放交互式 add 的终端输入序列，确认 5 个 * 的 cron 与含空格命令
// 都能整行保留，不会被按空白切分。
func TestCollectAddInputRegression(t *testing.T) {
	input := strings.Join([]string{
		"",                     // 任务ID：回车自动生成
		"test_write",           // 名称
		"* * * * *",            // 用户实际输入的 5 个星号
		`echo aaa >> c:\b.txt`, // 用户实际输入的含空格命令
		"",                     // 工作目录留空
		"0",                    // 超时
		"0",                    // 重试
		"y",                    // 启用
	}, "\r\n") + "\r\n"

	in := collectAddInput(bufio.NewReader(strings.NewReader(input)), "job-3")

	if in.Cron != "* * * * *" {
		t.Errorf("cron 被截断或改写: %q", in.Cron)
	}
	if in.Command != `echo aaa >> c:\b.txt` {
		t.Errorf("命令被截断或改写: %q", in.Command)
	}
	if in.WorkDir != "" {
		t.Errorf("work_dir 不应残留星号，实际 %q", in.WorkDir)
	}
	if in.ID != "job-3" {
		t.Errorf("自动 ID 不正确: %q", in.ID)
	}
	if in.Name != "test_write" {
		t.Errorf("名称不正确: %q", in.Name)
	}
	if in.Timeout != 0 || in.MaxRetry != 0 || !in.Enabled {
		t.Errorf("数字/布尔字段解析错误: %+v", in)
	}

	if err := cronexpr.Validate(in.Cron); err != nil {
		t.Errorf("用户输入的 5 字段表达式应通过校验: %v", err)
	}
	normalized, err := cronexpr.Parse(in.Cron)
	if err != nil || normalized != "0 * * * * *" {
		t.Errorf("归一化结果错误: %q, %v", normalized, err)
	}

	job := buildJobFromInput(in)
	if job.Cron != "* * * * *" || job.Command != `echo aaa >> c:\b.txt` || job.WorkDir != "" {
		t.Errorf("生成的 JobConfig 不正确: %+v", job)
	}
}

func TestCollectAddInputSixFieldAndWorkDir(t *testing.T) {
	input := strings.Join([]string{
		"my-job", "备份", "*/10 * * * * *", `C:\scripts\backup.bat`, `D:\work`, "120", "2", "n",
	}, "\n") + "\n"
	in := collectAddInput(bufio.NewReader(strings.NewReader(input)), "fallback")
	if in.ID != "my-job" || in.Cron != "*/10 * * * * *" {
		t.Errorf("6 字段输入解析错误: %+v", in)
	}
	if in.WorkDir != `D:\work` || in.Timeout != 120 || in.MaxRetry != 2 || in.Enabled {
		t.Errorf("字段解析错误: %+v", in)
	}
}

func TestReadInt(t *testing.T) {
	r := bufio.NewReader(strings.NewReader("\n30\n"))
	if got := readInt(r, "", 7); got != 7 {
		t.Errorf("空输入应使用默认值，实际 %d", got)
	}
	if got := readInt(r, "", 7); got != 30 {
		t.Errorf("数字输入解析错误，实际 %d", got)
	}

	bad := bufio.NewReader(strings.NewReader("abc\nxyz\nfoo\n"))
	if got := readInt(bad, "", 9); got != 9 {
		t.Errorf("连续非法输入应回退默认值，实际 %d", got)
	}
}

func TestReorderArgs(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{
			"位置参数在 flag 之前",
			[]string{"job-1", "-config", `D:\crontab.json`},
			[]string{"-config", `D:\crontab.json`, "job-1"},
		},
		{
			"flag 已在最前保持不变",
			[]string{"-cmd", "echo hello"},
			[]string{"-cmd", "echo hello"},
		},
		{
			"等号形式",
			[]string{"job-1", "-config=x.json"},
			[]string{"-config=x.json", "job-1"},
		},
		{
			"多个位置参数保持顺序",
			[]string{"a", "b", "-cmd", "x"},
			[]string{"-cmd", "x", "a", "b"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := reorderArgs(tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("reorderArgs(%v) = %v, 期望 %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestNextJobID(t *testing.T) {
	jobs := []config.JobConfig{
		{ID: "job-1"}, {ID: "job-2"}, {ID: "job-4"},
	}
	if got := nextJobID(jobs); got != "job-3" {
		t.Errorf("应返回最小可用 ID job-3，实际 %q", got)
	}
}
