//go:build !windows

package doctor

import "os"

// fillWindowsInfo 非Windows平台的占位实现
func fillWindowsInfo(info *EnvInfo) {
	info.IsInteractive = true
	info.IsService = false

	// Linux/Mac 下列出根目录挂载点作为参考
	if entries, err := os.ReadDir("/"); err == nil {
		for _, e := range entries {
			if e.IsDir() {
				info.Drives = append(info.Drives, DriveInfo{
					Letter: "/" + e.Name(),
					Type:   "dir",
					Note:   "目录",
				})
			}
		}
	}
}
