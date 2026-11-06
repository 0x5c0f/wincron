// Package cronexpr 提供 cron 表达式的归一化与校验。
// 独立成包，供 config（落盘前校验）、scheduler（调度前归一化）、web（API 前置校验）共用，
// 避免底层 config 反向依赖 scheduler。
package cronexpr

import (
	"fmt"
	"strings"

	"github.com/robfig/cron/v3"
)

// parser 统一使用 6 字段（秒 分 时 日 月 周）解析器
var parser = cron.NewParser(
	cron.Second | cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor,
)

// Normalize 把用户写的 cron 表达式归一化为 6 字段形式。
//
//   - 5 字段（分 时 日 月 周）：前面补 "0 "，语义保持"每分钟执行一次"
//   - 6 字段（秒 分 时 日 月 周）：原样
//   - 其他字段数：报错
//
// 这样用户不必关心 enable_seconds 开关，Linux crontab 习惯的写法直接可用。
func Normalize(expr string) (string, error) {
	trimmed := strings.TrimSpace(expr)

	// @every 1m / @daily / @hourly 等描述符不是按字段组织的，
	// 直接交给 robfig 原生解析器（WithSeconds 的 parser 含 Descriptor 位）。
	if strings.HasPrefix(trimmed, "@") {
		if _, err := parser.Parse(trimmed); err != nil {
			return "", fmt.Errorf("cron 描述符 %q 语法错误: %w", expr, err)
		}
		return trimmed, nil
	}

	fields := strings.Fields(trimmed)
	switch len(fields) {
	case 5:
		return "0 " + strings.Join(fields, " "), nil
	case 6:
		return strings.Join(fields, " "), nil
	default:
		return "", fmt.Errorf(
			"cron 表达式需要 5 个字段(分 时 日 月 周)或 6 个字段(秒 分 时 日 月 周)，当前解析到 %d 个: %q",
			len(fields), expr)
	}
}

// Validate 校验表达式是否合法（字段数 + 语法），返回可直接阅读的中文错误
func Validate(expr string) error {
	normalized, err := Normalize(expr)
	if err != nil {
		return err
	}
	if _, err := parser.Parse(normalized); err != nil {
		return fmt.Errorf("cron 表达式 %q 语法错误: %w", expr, err)
	}
	return nil
}

// Parse 返回可用于 robfig/cron AddFunc 的 6 字段表达式
func Parse(expr string) (string, error) {
	normalized, err := Normalize(expr)
	if err != nil {
		return "", err
	}
	if _, err := parser.Parse(normalized); err != nil {
		return "", fmt.Errorf("cron 表达式 %q 语法错误: %w", expr, err)
	}
	return normalized, nil
}
