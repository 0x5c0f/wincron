// Package paths 统一路径解析基准。
//
// 关键背景：Windows 服务由 SCM 启动时，工作目录是 %SystemRoot%\System32，
// 不是 exe 所在目录。任何相对路径（logs/、web/static/、默认 crontab.json）
// 若按 os.Getwd() 解析，都会被写到 System32，导致：
//   - 日志写到系统目录（可能无权限）
//   - 服务写的日志和 CLI 读的日志不是同一份
//   - 默认配置文件在不同进程里指向不同文件
//
// 因此所有内部相对路径一律以「可执行文件所在目录」为基准。
package paths

import (
	"os"
	"path/filepath"
	"sync"
)

var (
	baseDirOnce sync.Once
	baseDir     string
)

// BaseDir 返回可执行文件所在目录
func BaseDir() string {
	baseDirOnce.Do(func() {
		exe, err := os.Executable()
		if err != nil {
			wd, _ := os.Getwd()
			baseDir = wd
			return
		}
		baseDir = filepath.Dir(exe)
	})
	return baseDir
}

// Resolve 解析路径：绝对路径原样返回；相对路径基于可执行文件目录
func Resolve(p string) string {
	if p == "" {
		return ""
	}
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Join(BaseDir(), p)
}

// DefaultConfigPath 返回默认配置文件绝对路径
func DefaultConfigPath() string {
	return Resolve("crontab.json")
}
