package executor

import (
	"unicode/utf8"

	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"
)

// decodeOutput 把子进程输出解码为 UTF-8 字符串
//
// 背景（中文 Windows 必踩的坑）：
//
//	cmd /c 启动的 bat 脚本，stdout/stderr 默认使用系统 OEM 代码页，
//	简体中文 Windows 是 GBK(cp936)。Go 直接 string(b) 会得到乱码，
//	Web 界面和日志文件里全是乱码字符。
//
// 处理策略：
//  1. 已经是合法 UTF-8（含纯 ASCII）→ 原样返回
//  2. 否则尝试 GBK → UTF-8
//  3. 再退一步尝试 GB18030（GBK 超集）
//  4. 都失败则原样返回，至少不丢字节
func decodeOutput(b []byte) string {
	if len(b) == 0 {
		return ""
	}

	if utf8.Valid(b) {
		return string(b)
	}

	if s, ok := transcode(simplifiedchinese.GBK, b); ok {
		return s
	}

	if s, ok := transcode(simplifiedchinese.GB18030, b); ok {
		return s
	}

	return string(b)
}

func transcode(enc encoding.Encoding, b []byte) (string, bool) {
	out, _, err := transform.Bytes(enc.NewDecoder(), b)
	if err != nil || !utf8.Valid(out) {
		return "", false
	}
	return string(out), true
}
