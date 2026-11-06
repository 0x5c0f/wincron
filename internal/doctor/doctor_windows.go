//go:build windows

package doctor

import (
	"os/user"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

const driveRemote = 0x00000004 // DRIVE_REMOTE

var (
	modkernel32            = windows.NewLazySystemDLL("kernel32.dll")
	procGetDriveType       = modkernel32.NewProc("GetDriveTypeW")
	procProcessIdToSession = modkernel32.NewProc("ProcessIdToSessionId")
)

// fillWindowsInfo 采集 Windows 特有的会话与驱动器信息
func fillWindowsInfo(info *EnvInfo) {
	// 当前进程所属 Session ID
	// Session 0 = 服务会话（Vista 起的 Session 0 隔离）
	// Session 1+ = 交互式登录用户会话
	var sessionID uint32
	r, _, _ := procProcessIdToSession.Call(
		uintptr(info.ProcessID),
		uintptr(unsafe.Pointer(&sessionID)),
	)
	if r != 0 {
		info.SessionID = sessionID
	}
	info.IsInteractive = sessionID != 0
	info.IsService = sessionID == 0

	// 补全带域名的账户名（os/user 在 SYSTEM 下可能只给短名）
	if u, err := user.Current(); err == nil && u.Username != "" {
		info.UserName = u.Username
	}

	// 逻辑驱动器位掩码
	mask, err := windows.GetLogicalDrives()
	if err != nil {
		return
	}

	for i := 0; i < 26; i++ {
		if mask&(1<<uint(i)) == 0 {
			continue
		}
		letter := string(rune('A' + i))
		root, e := syscall.UTF16PtrFromString(letter + `:\`)
		if e != nil {
			continue
		}
		t, _, _ := procGetDriveType.Call(uintptr(unsafe.Pointer(root)))
		d := DriveInfo{Letter: letter + ":"}
		switch uint32(t) {
		case windows.DRIVE_FIXED:
			d.Type = "fixed"
			d.Note = "本地磁盘"
		case driveRemote:
			d.Type = "remote"
			d.Note = "网络映射盘(按登录会话建立，服务中易失效)"
		case windows.DRIVE_CDROM:
			d.Type = "cdrom"
			d.Note = "光驱"
		case windows.DRIVE_REMOVABLE:
			d.Type = "removable"
			d.Note = "可移动磁盘"
		case windows.DRIVE_RAMDISK:
			d.Type = "ramdisk"
			d.Note = "内存盘"
		default:
			d.Type = "unknown"
			d.Note = "其他"
		}
		info.Drives = append(info.Drives, d)
	}
}
