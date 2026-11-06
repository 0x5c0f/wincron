package executor

import (
	"os"
	"path/filepath"
)

// openAppend 打开文件用于追加写入，如果目录不存在则创建
func openAppend(path string) (*os.File, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}
	return os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
}
