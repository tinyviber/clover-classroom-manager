// Package lanhttp 把 labsvc.Lab 挂到 net/http 上：同一套管理端 UI 走浏览器
// 而不是 mygo WebView——供 headless 管理端（如 Windows 7 教师机）使用。
//
// 端点：
//
//	POST /api/call            {"m":"Lab.Devices","a":[...]} → 反射调用 Lab 方法
//	GET  /api/events?k=TOKEN  SSE：{type:"devices"|"classroom",data:...}
//	POST /api/upload?name=    浏览器选中的分发文件 → 服务器落盘，返回 {path}
//	GET  /api/client-package?addr=&k= → 生成安装包 zip 下载
//	GET  /*                   静态前端（*.html 注入 __LAB_HEADLESS__ token）
//
// 鉴权：token 每次启动随机生成，本身就是管理凭据——只注入回环客户端
// 或自带有效 ?k 的页面；/api/* 走 X-Lab-Token 头，SSE/下载走 ?k
// （EventSource/链接不能带头），写请求校验 Origin 同源。绑定非回环
// 地址时 LAN 用户必须持带 ?k 的 URL（启动时打印在管理端控制台）。
package lanhttp

import (
	"archive/zip"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/tinyviber/clover-classroom-manager/internal/labsvc"
)

const maxUpload = 4 << 30 // 教师分发文件上限 4GB

// Server 是 labsvc.Host 的 headless 实现 + HTTP 桥：事件经 SSE 推给浏览器。
type Server struct {
	lab       *labsvc.Lab
	token     string
	origin    string
	uploadDir string
	static    fs.FS
	mux       *http.ServeMux
	methods   map[string]reflect.Value

	hubMu sync.Mutex
	subs  map[chan []byte]struct{}
}

// New 创建桥。static 为前端 dist（可为 nil=只开 API）；origin 用于 FocusAdmin 唤起。
// 之后调用 Bind 挂上 Lab 方法集（labsvc.New 需要 host，先建 Server 再回绑）。
func New(static fs.FS, uploadDir, origin string) (*Server, error) {
	token := make([]byte, 16)
	if _, err := rand.Read(token); err != nil {
		return nil, err
	}
	s := &Server{
		token:     hex.EncodeToString(token),
		origin:    origin,
		uploadDir: uploadDir,
		static:    static,
		mux:       http.NewServeMux(),
		methods:   map[string]reflect.Value{},
		subs:      map[chan []byte]struct{}{},
	}
	s.mux.HandleFunc("/api/call", s.withAuth(s.call))
	s.mux.HandleFunc("/api/upload", s.withAuth(s.upload))
	s.mux.HandleFunc("/api/client-package", s.withAuth(s.clientPackage))
	s.mux.HandleFunc("/api/events", s.events)
	s.mux.HandleFunc("/", s.serveStatic)
	return s, nil
}

// httpDeny 里列出的导出方法只对桌面端（mygo 绑定）开放，不挂到 HTTP RPC：
// 这类方法接受管理端本机路径参数或只做进程内用途，经网络暴露会扩大攻击面。
var httpDeny = map[string]bool{
	"ExportClientPackageTo": true, // 参数是服务器侧任意目录，HTTP 侧走固定下载端点
}

// Bind 把 Lab 的导出方法集挂到 /api/call 反射分发上（httpDeny 除外）。
func (s *Server) Bind(lab *labsvc.Lab) {
	s.lab = lab
	lv := reflect.ValueOf(lab)
	lt := lv.Type()
	for i := 0; i < lt.NumMethod(); i++ {
		m := lt.Method(i)
		if httpDeny[m.Name] {
			continue
		}
		s.methods["Lab."+m.Name] = lv.Method(i)
	}
}

// Token 是注入页面的访问令牌（仅用于校验，不进入 URL）。
func (s *Server) Token() string { return s.token }

// Handler 返回完整 HTTP 处理器。
func (s *Server) Handler() http.Handler { return s.mux }

// ---- labsvc.Host 实现 ---------------------------------------------------------

func (s *Server) MinimizeMain() {}
func (s *Server) ShowMain()     {}
func (s *Server) OpenCastBar()  {}
func (s *Server) CloseCastBar() {}

func (s *Server) FocusAdmin() {
	if err := OpenBrowser(s.origin); err != nil {
		log.Printf("lanhttp: open %s: %v", s.origin, err)
	}
}

func (s *Server) PickFiles() []string   { return nil }
func (s *Server) PickExportDir() string { return "" }

func (s *Server) OpenPath(dir string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("explorer", dir)
	case "darwin":
		cmd = exec.Command("open", dir)
	default:
		cmd = exec.Command("xdg-open", dir)
	}
	if err := cmd.Start(); err != nil {
		log.Printf("lanhttp: open path %s: %v", dir, err)
	}
}

// ---- 事件推送（SSE）------------------------------------------------------------

func (s *Server) EmitDevices(devs []labsvc.Device)       { s.push("devices", devs) }
func (s *Server) EmitClassroom(st labsvc.ClassroomState) { s.push("classroom", st) }

func (s *Server) push(eventType string, data any) {
	payload, err := json.Marshal(map[string]any{"type": eventType, "data": data})
	if err != nil {
		return
	}
	frame := append([]byte("data: "), payload...)
	frame = append(frame, '\n', '\n')
	s.hubMu.Lock()
	defer s.hubMu.Unlock()
	for ch := range s.subs {
		select {
		case ch <- frame:
		default: // 慢客户端丢帧——课堂快照是周期推送，下一帧会补上
		}
	}
}

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("k") != s.token {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "no streaming", http.StatusInternalServerError)
		return
	}
	ch := make(chan []byte, 4)
	s.hubMu.Lock()
	s.subs[ch] = struct{}{}
	s.hubMu.Unlock()
	defer func() {
		s.hubMu.Lock()
		delete(s.subs, ch)
		s.hubMu.Unlock()
	}()
	fmt.Fprint(w, ": connected\n\n")
	flusher.Flush()
	for {
		select {
		case frame := <-ch:
			if _, err := w.Write(frame); err != nil {
				return
			}
			flusher.Flush()
		case <-r.Context().Done():
			return
		case <-time.After(25 * time.Second):
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// ---- RPC 调用 ------------------------------------------------------------------

type callReq struct {
	Method string            `json:"m"`
	Args   []json.RawMessage `json:"a"`
}

func (s *Server) call(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var req callReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&req); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	fn, ok := s.methods[req.Method]
	if !ok && !strings.Contains(req.Method, ".") {
		fn, ok = s.methods["Lab."+req.Method]
	}
	if !ok {
		http.Error(w, "unknown method", http.StatusNotFound)
		return
	}
	mt := fn.Type()
	if len(req.Args) != mt.NumIn() {
		http.Error(w, fmt.Sprintf("want %d args, got %d", mt.NumIn(), len(req.Args)), http.StatusBadRequest)
		return
	}
	in := make([]reflect.Value, mt.NumIn())
	for i := range in {
		arg := reflect.New(mt.In(i))
		if err := json.Unmarshal(req.Args[i], arg.Interface()); err != nil {
			http.Error(w, fmt.Sprintf("arg %d: %v", i, err), http.StatusBadRequest)
			return
		}
		in[i] = arg.Elem()
	}
	var out []reflect.Value
	func() {
		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("lanhttp: call %s panic: %v", req.Method, rec)
				out = nil
			}
		}()
		out = fn.Call(in)
	}()
	if out == nil && mt.NumOut() > 0 {
		http.Error(w, "call failed", http.StatusInternalServerError)
		return
	}
	errType := reflect.TypeOf((*error)(nil)).Elem()
	var result any
	for i, v := range out {
		// 最后一个 error 返回值非 nil 视为调用失败——此前会把 error 序列化成 {} 吞掉
		if i == len(out)-1 && v.Type().Implements(errType) {
			if !v.IsNil() {
				http.Error(w, v.Interface().(error).Error(), http.StatusInternalServerError)
				return
			}
			continue
		}
		if result == nil {
			result = v.Interface()
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result)
}

// ---- 上传 / 导出 ---------------------------------------------------------------

func (s *Server) upload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	name := filepath.Base(r.URL.Query().Get("name"))
	if name == "" || name == "." || name == "/" {
		http.Error(w, "bad name", http.StatusBadRequest)
		return
	}
	if err := os.MkdirAll(s.uploadDir, 0o700); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	dst := filepath.Join(s.uploadDir, fmt.Sprintf("%d-%s", time.Now().UnixNano(), name))
	f, err := os.Create(dst)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer f.Close()
	if _, err := io.Copy(f, http.MaxBytesReader(w, r.Body, maxUpload)); err != nil {
		_ = os.Remove(dst)
		http.Error(w, err.Error(), http.StatusRequestEntityTooLarge)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"path": dst})
}

func (s *Server) clientPackage(w http.ResponseWriter, r *http.Request) {
	dir, err := os.MkdirTemp("", "client-package-*")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer os.RemoveAll(dir)
	pkg := filepath.Join(dir, "client-package")
	if err := s.lab.ExportClientPackageTo(pkg, r.URL.Query().Get("addr")); err != nil {
		log.Printf("lanhttp: 生成安装包失败: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="client-package.zip"`)
	zw := zip.NewWriter(w)
	_ = filepath.Walk(pkg, func(p string, info fs.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(pkg, p)
		zf, err := zw.Create(rel)
		if err != nil {
			return err
		}
		src, err := os.Open(p)
		if err != nil {
			return err
		}
		defer src.Close()
		_, err = io.Copy(zf, src)
		return err
	})
	_ = zw.Close()
}

// ---- 静态前端 + token 注入 --------------------------------------------------------

func (s *Server) withAuth(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// 跨站防护：带 Origin 的写请求必须同源（?k 凭据在 URL 里时防 CSRF）
		if o := r.Header.Get("Origin"); o != "" {
			if u, err := url.Parse(o); err != nil || u.Host != r.Host {
				http.Error(w, "cross-origin", http.StatusForbidden)
				return
			}
		}
		if r.Header.Get("X-Lab-Token") != s.token && r.URL.Query().Get("k") != s.token {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		h(w, r)
	}
}

// clientOK 判断该请求是否有资格拿到注入 token 的页面：
// 回环客户端（本机浏览器）直接信任；局域网请求必须自带有效 ?k 凭据——
// token 就是管理凭据，不能发给任何能打开页面的人。
func (s *Server) clientOK(r *http.Request) bool {
	if r.URL.Query().Get("k") == s.token {
		return true
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	return err == nil && net.ParseIP(host).IsLoopback()
}

func (s *Server) serveStatic(w http.ResponseWriter, r *http.Request) {
	if s.static == nil {
		http.NotFound(w, r)
		return
	}
	name := strings.TrimPrefix(filepath.Clean("/"+r.URL.Path), "/")
	if name == "" || name == "/" {
		name = "index.html"
	}
	data, err := fs.ReadFile(s.static, name)
	if err != nil {
		// SPA 回退：非 asset 路径一律给 index.html
		if !strings.Contains(name, ".") {
			data, err = fs.ReadFile(s.static, "index.html")
		}
		if err != nil {
			http.NotFound(w, r)
			return
		}
		name = "index.html"
	}
	if strings.HasSuffix(name, ".html") {
		if !s.clientOK(r) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		inject := fmt.Sprintf(`<script>window.__LAB_HEADLESS__={token:%q};</script>`, s.token)
		data = injectHead(data, inject)
	}
	http.ServeContent(w, r, name, time.Now(), strings.NewReader(string(data)))
}

func injectHead(html []byte, snippet string) []byte {
	if i := strings.Index(strings.ToLower(string(html)), "<head>"); i >= 0 {
		return append(append(append([]byte{}, html[:i+6]...), snippet...), html[i+6:]...)
	}
	return append([]byte(snippet), html...)
}

// OpenBrowser 用系统默认浏览器打开 url（headless 唤起管理页）。
func OpenBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	return cmd.Start()
}
