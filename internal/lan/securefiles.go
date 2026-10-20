package lan

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

var safeTransferID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,96}$`)

// incoming writes a transfer to a private temporary file and only publishes it
// after its size, ordered chunks, and SHA-256 digest have all been verified.
type incoming struct {
	mu           sync.Mutex
	f            *os.File
	tmpPath      string
	dir          string
	name         string
	expectedSize int64
	written      int64
	nextSeq      int
	sum          hash.Hash
	devID        string
	transferID   string
	at           time.Time
}

func (in *incoming) progress() (int, int64) {
	if in == nil {
		return 0, 0
	}
	in.mu.Lock()
	defer in.mu.Unlock()
	return in.nextSeq, in.written
}

func beginIncoming(root, ownerID, transferID, name string, size int64) (*incoming, error) {
	if !safeTransferID.MatchString(transferID) {
		return nil, fmt.Errorf("非法传输编号")
	}
	if size < 0 || size > MaxTransferSize {
		return nil, fmt.Errorf("文件大小超出限制")
	}
	dir, err := safeReceiveDir(root, ownerID)
	if err != nil {
		return nil, err
	}
	f, err := os.CreateTemp(dir, ".lab-"+transferID+"-*.part")
	if err != nil {
		return nil, err
	}
	return &incoming{
		f: f, tmpPath: f.Name(), dir: dir, name: safeFilename(name),
		expectedSize: size, sum: sha256.New(), devID: ownerID, transferID: transferID, at: time.Now(),
	}, nil
}

func (in *incoming) writeChunk(seq int, data []byte) error {
	if in == nil {
		return fmt.Errorf("传输不存在")
	}
	in.mu.Lock()
	defer in.mu.Unlock()
	if in.f == nil {
		return fmt.Errorf("传输不存在")
	}
	if seq != in.nextSeq {
		return fmt.Errorf("数据块序号不连续")
	}
	if len(data) == 0 || len(data) > FileChunk {
		return fmt.Errorf("数据块大小非法")
	}
	if in.written+int64(len(data)) > in.expectedSize {
		return fmt.Errorf("接收数据超过声明大小")
	}
	n, err := in.f.Write(data)
	if err != nil {
		return err
	}
	if n != len(data) {
		return io.ErrShortWrite
	}
	_, _ = in.sum.Write(data)
	in.written += int64(n)
	in.nextSeq++
	return nil
}

func (in *incoming) finish(count int, digest string) (string, error) {
	if in == nil {
		return "", fmt.Errorf("传输不存在")
	}
	in.mu.Lock()
	defer in.mu.Unlock()
	if in.f == nil {
		return "", fmt.Errorf("传输不存在")
	}
	actual := hex.EncodeToString(in.sum.Sum(nil))
	if count != in.nextSeq || in.written != in.expectedSize || !strings.EqualFold(actual, digest) {
		in.abortLocked()
		return "", fmt.Errorf("文件校验失败（大小、序号或 SHA-256 不匹配）")
	}
	if err := in.f.Sync(); err != nil {
		in.abortLocked()
		return "", err
	}
	if err := in.f.Close(); err != nil {
		in.f = nil
		_ = os.Remove(in.tmpPath)
		return "", err
	}
	in.f = nil
	finalPath, err := publishUnique(in.tmpPath, in.dir, in.name)
	if err != nil {
		_ = os.Remove(in.tmpPath)
		return "", err
	}
	return filepath.Base(finalPath), nil
}

func (in *incoming) abort() {
	if in == nil {
		return
	}
	in.mu.Lock()
	defer in.mu.Unlock()
	in.abortLocked()
}

func (in *incoming) abortLocked() {
	if in == nil {
		return
	}
	if in.f != nil {
		_ = in.f.Close()
		in.f = nil
	}
	if in.tmpPath != "" {
		_ = os.Remove(in.tmpPath)
	}
}

func safeReceiveDir(root, ownerID string) (string, error) {
	root, err := safeReceiveRoot(root)
	if err != nil {
		return "", err
	}
	owner := safeOwnerID(ownerID)
	dir := filepath.Join(root, owner)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("接收目录不能是符号链接")
	}
	dir, err = filepath.EvalSymlinks(dir)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, dir)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("接收目录越界")
	}
	return dir, nil
}

func safeReceiveRoot(root string) (string, error) {
	if strings.TrimSpace(root) == "" {
		return "", fmt.Errorf("未配置接收目录")
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(root)
}

type receiveUsage struct {
	bytes int64
	files int
}

// scanReceiveUsage counts retained regular files without following symlinks.
// Incomplete .part files are counted by active reservations instead.
func scanReceiveUsage(root string) (receiveUsage, map[string]receiveUsage, error) {
	root, err := safeReceiveRoot(root)
	if err != nil {
		return receiveUsage{}, nil, err
	}
	var total receiveUsage
	byOwner := make(map[string]receiveUsage)
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root || entry.IsDir() {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 || (strings.HasPrefix(entry.Name(), ".lab-") && strings.HasSuffix(entry.Name(), ".part")) {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		owner := ""
		if head, _, found := strings.Cut(rel, string(filepath.Separator)); found {
			owner = head
		}
		usage := byOwner[owner]
		usage.bytes += info.Size()
		usage.files++
		byOwner[owner] = usage
		total.bytes += info.Size()
		total.files++
		return nil
	})
	return total, byOwner, err
}

func safeOwnerID(id string) string {
	if id != "" && safeTransferID.MatchString(id) {
		return id
	}
	h := sha256.Sum256([]byte(id))
	return "device-" + hex.EncodeToString(h[:12])
}

func safeFilename(name string) string {
	name = strings.ReplaceAll(name, `\`, `/`)
	name = filepath.Base(name)
	name = strings.TrimSpace(name)
	var b strings.Builder
	for _, r := range name {
		switch {
		case r < 32 || strings.ContainsRune(`<>:"/\|?*`, r):
			b.WriteRune('_')
		default:
			b.WriteRune(r)
		}
	}
	name = strings.Trim(b.String(), " .")
	if name == "" || name == "." || name == ".." {
		name = "upload.bin"
	}
	if len([]rune(name)) > 120 {
		runes := []rune(name)
		name = string(runes[:120])
	}
	base := strings.ToUpper(strings.TrimSuffix(name, filepath.Ext(name)))
	switch base {
	case "CON", "PRN", "AUX", "NUL", "COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9", "LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9":
		name = "_" + name
	}
	return name
}

// publishUnique links the fully written staging file to a unique final name.
// Link is atomic and fails rather than replacing an existing file on all
// supported platforms; the staging file lives in the same directory.
func publishUnique(tempPath, dir, name string) (string, error) {
	name = safeFilename(name)
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	for i := 0; i < 100; i++ {
		suffix := time.Now().Format("20060102-150405.000000000") + "-" + RandHex(5)
		if i > 0 {
			suffix += fmt.Sprintf("-%d", i)
		}
		candidate := filepath.Join(dir, stem+"-"+suffix+ext)
		err := os.Link(tempPath, candidate)
		if err == nil {
			_ = os.Remove(tempPath)
			return candidate, nil
		}
		if !os.IsExist(err) {
			return "", err
		}
		time.Sleep(time.Nanosecond)
	}
	return "", fmt.Errorf("无法生成唯一文件名")
}

func cleanupPartialFiles(root string) {
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if strings.HasPrefix(d.Name(), ".lab-") && strings.HasSuffix(d.Name(), ".part") {
			_ = os.Remove(path)
		}
		return nil
	})
}

// CleanIncompleteFiles removes transfer staging files left by an earlier process run.
func CleanIncompleteFiles(root string) { cleanupPartialFiles(root) }
