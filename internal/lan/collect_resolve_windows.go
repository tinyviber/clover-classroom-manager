//go:build windows

package lan

import "golang.org/x/sys/windows"

// resolveCollectPath 返回跟随符号链接和目录联接之后的最终 DOS 路径。
//
// filepath.EvalSymlinks 从 Go 1.23 起不跟随目录联接：IO_REPARSE_TAG_MOUNT_POINT
// 被标成 ModeIrregular，不是 ModeSymlink，walkSymlinks 会把联接路径原样返回。
// 学生在作业目录里 mklink /J 指到白名单外时，只靠 EvalSymlinks 的检查会被绕过。
// CreateFile 默认跟随重解析点，GetFinalPathNameByHandle 给出目标的真实路径。
func resolveCollectPath(p string) (string, error) {
	pathp, err := windows.UTF16PtrFromString(p)
	if err != nil {
		return "", err
	}
	h, err := windows.CreateFile(pathp, 0,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return "", err
	}
	defer windows.CloseHandle(h)

	// VOLUME_NAME_DOS = 0，结果形如 \\?\C:\...，normalizeCollectPath 会剥掉前缀。
	buf := make([]uint16, 512)
	for {
		n, err := windows.GetFinalPathNameByHandle(h, &buf[0], uint32(len(buf)), 0)
		if err != nil {
			return "", err
		}
		if n > 0 && n < uint32(len(buf)) {
			return windows.UTF16ToString(buf[:n]), nil
		}
		need := n
		if need <= uint32(len(buf)) {
			need = uint32(len(buf)) * 2
		}
		buf = make([]uint16, need)
	}
}
