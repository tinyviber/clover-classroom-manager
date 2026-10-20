package studentui

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/tinyviber/clover-classroom-manager/internal/lan"
)

const maxUpload = lan.MaxStudentUploadSize

type server struct {
	agent       *lan.Agent
	token       string
	origin      string
	uploadSlots chan struct{}
}

func Start(ctx context.Context, agent *lan.Agent) error {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return err
	}
	tokenBytes := make([]byte, 24)
	if _, err := rand.Read(tokenBytes); err != nil {
		_ = listener.Close()
		return err
	}
	port := listener.Addr().(*net.TCPAddr).Port
	origin := fmt.Sprintf("http://127.0.0.1:%d", port)
	ui := &server{agent: agent, token: hex.EncodeToString(tokenBytes), origin: origin, uploadSlots: make(chan struct{}, 2)}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", ui.page)
	mux.HandleFunc("GET /api/state", ui.state)
	mux.HandleFunc("POST /api/signin", ui.signIn)
	mux.HandleFunc("POST /api/poll", ui.poll)
	mux.HandleFunc("POST /api/help", ui.help)
	mux.HandleFunc("POST /api/demo", ui.demo)
	mux.HandleFunc("POST /api/demo/stop", ui.demoStop)
	mux.HandleFunc("POST /api/upload", ui.upload)
	httpServer := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 3 * time.Second,
		ReadTimeout:       30 * time.Minute,
		WriteTimeout:      30 * time.Minute,
		MaxHeaderBytes:    8 << 10,
	}
	go func() {
		if err := httpServer.Serve(listener); err != nil && err != http.ErrServerClosed {
			log.Printf("student UI: %v", err)
		}
	}()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdownCtx)
	}()
	url := origin + "/"
	log.Printf("student UI: open %s in the logged-in user's browser", url)
	if err := openBrowser(url); err != nil {
		log.Printf("student UI: open this address in a browser: %s (%v)", url, err)
	}
	return nil
}

func (s *server) page(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = io.WriteString(w, strings.Replace(studentPage, "__LAB_TOKEN__", s.token, 1))
}

func (s *server) state(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(w, r) {
		return
	}
	writeJSON(w, http.StatusOK, s.agent.StudentState())
}

func (s *server) signIn(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(w, r) {
		return
	}
	var action struct {
		Session string `json:"session"`
	}
	if !readJSON(w, r, &action) || len(action.Session) > 100 {
		return
	}
	s.action(w, s.agent.AcknowledgeSignIn(action.Session))
}

func (s *server) poll(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(w, r) {
		return
	}
	var action struct {
		Session string `json:"session"`
		Option  string `json:"option"`
	}
	if !readJSON(w, r, &action) || len(action.Session) > 100 || len([]rune(action.Option)) > 100 {
		return
	}
	s.action(w, s.agent.Vote(action.Session, action.Option))
}

func (s *server) help(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(w, r) {
		return
	}
	s.action(w, s.agent.RaiseHand())
}

func (s *server) demo(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(w, r) {
		return
	}
	var action struct {
		Session string `json:"session"`
		Accept  bool   `json:"accept"`
	}
	if !readJSON(w, r, &action) || len(action.Session) > 100 {
		return
	}
	s.action(w, s.agent.DemoConsent(action.Session, action.Accept))
}

func (s *server) demoStop(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(w, r) {
		return
	}
	s.action(w, s.agent.StopStudentDemo())
}

func (s *server) upload(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(w, r) {
		return
	}
	select {
	case s.uploadSlots <- struct{}{}:
		defer func() { <-s.uploadSlots }()
	default:
		http.Error(w, "同时只能提交 2 个文件，请稍后重试", http.StatusTooManyRequests)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxUpload+(1<<20))
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		http.Error(w, "文件上传超过 128 MiB 或表单无效", http.StatusRequestEntityTooLarge)
		return
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "请选择要提交的文件", http.StatusBadRequest)
		return
	}
	defer file.Close()
	if header.Size < 0 || header.Size > maxUpload {
		http.Error(w, "文件大小超过 128 MiB", http.StatusRequestEntityTooLarge)
		return
	}
	tmp, err := os.CreateTemp("", "lab-student-upload-*.part")
	if err != nil {
		http.Error(w, "无法准备临时文件", http.StatusInternalServerError)
		return
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	n, copyErr := io.Copy(tmp, io.LimitReader(file, maxUpload+1))
	closeErr := tmp.Close()
	if copyErr != nil || closeErr != nil || n > maxUpload || n != header.Size {
		http.Error(w, "文件读取失败或大小不匹配", http.StatusBadRequest)
		return
	}
	if err := s.agent.SubmitFile(tmpName, header.Filename); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "name": header.Filename})
}

func (s *server) authorized(w http.ResponseWriter, r *http.Request) bool {
	remote, _, err := net.SplitHostPort(r.RemoteAddr)
	ip := net.ParseIP(remote)
	if err != nil || ip == nil || !ip.IsLoopback() {
		http.Error(w, "loopback only", http.StatusForbidden)
		return false
	}
	if origin := r.Header.Get("Origin"); origin != "" && origin != s.origin {
		http.Error(w, "origin mismatch", http.StatusForbidden)
		return false
	}
	if r.Header.Get("X-Lab-Token") != s.token {
		http.Error(w, "invalid local session", http.StatusForbidden)
		return false
	}
	return true
}

func (s *server) action(w http.ResponseWriter, err error) {
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func readJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	defer r.Body.Close()
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		http.Error(w, "invalid action", http.StatusBadRequest)
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func openBrowser(url string) error {
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

const studentPage = `<!doctype html>
<html lang="zh-CN"><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>三叶草局域网课堂管理 · 学生操作</title>
<style>
:root{color-scheme:light;font:15px/1.5 system-ui,-apple-system,"Segoe UI",sans-serif;color:#22252d;background:#f4f4f6}*{box-sizing:border-box}body{margin:0;padding:28px 16px;max-width:760px;margin-inline:auto}.top{display:flex;justify-content:space-between;align-items:center;margin-bottom:20px}.title{font-size:20px;font-weight:650}.connection{color:#7b8190;font-size:13px}.card{background:#fff;border:1px solid #e2e3e8;border-radius:12px;padding:18px;margin:12px 0;box-shadow:0 2px 10px #20243508}.card h2{font-size:16px;margin:0 0 8px}.muted{color:#747987;font-size:13px}.actions{display:flex;gap:8px;flex-wrap:wrap;margin-top:14px}button,.file-label{border:0;border-radius:8px;background:#3549a2;color:white;padding:9px 13px;font:inherit;cursor:pointer}button.secondary{background:#eceef5;color:#343846}button.danger{background:#a63e48}button:disabled{opacity:.5;cursor:default}.option{display:block;width:100%;text-align:left;margin:7px 0;background:#f0f1f6;color:#272b35}.option:hover{background:#e3e6f5}.hidden{display:none!important}.status{min-height:24px;color:#476442;margin-top:12px}.file-label{display:inline-flex;align-items:center}.file-label input{position:absolute;opacity:0;width:1px;height:1px}.foot{font-size:12px;color:#8b909c;margin:22px 2px}
</style>
<body><header class="top"><div><div class="title">三叶草局域网课堂管理</div><div id="identity" class="muted">学生操作</div></div><div id="connection" class="connection">连接中…</div></header>
<section id="signin" class="card hidden"><h2>课堂签到</h2><div class="muted">老师发起了签到，请确认到课。</div><div class="actions"><button id="signinButton">确认签到</button></div></section>
<section id="poll" class="card hidden"><h2 id="pollQuestion"></h2><div id="pollOptions"></div><div id="pollDeadline" class="muted"></div></section>
<section id="demo" class="card hidden"><h2>学生演示请求</h2><div id="demoText" class="muted"></div><div class="actions"><button id="demoAccept">同意并开始</button><button id="demoDecline" class="secondary">拒绝</button><button id="demoStop" class="danger hidden">停止我的演示</button></div></section>
<section class="card"><h2>需要老师帮助？</h2><div class="muted">举手后老师会在管理端看到你的设备编号。</div><div class="actions"><button id="helpButton">举手求助</button></div></section>
<section class="card"><h2>提交文件</h2><div class="muted">选择一份不超过 128 MiB 的文件发送给老师。</div><div class="actions"><label class="file-label">选择并提交文件<input id="file" type="file"></label></div></section>
<div id="status" class="status" role="status"></div><div class="foot">此页面仅在本机登录会话中运行。屏幕演示需你明确同意，老师或你都可以停止。</div>
<script>
const token="__LAB_TOKEN__",$=id=>document.getElementById(id);let state={},signId="",pollId="",demoId="";
async function api(path,body){const r=await fetch(path,{method:body===undefined?"GET":"POST",headers:{"X-Lab-Token":token,...(body===undefined?{}:{"Content-Type":"application/json"})},body:body===undefined?undefined:JSON.stringify(body)});if(!r.ok)throw new Error(await r.text());return r.json()}
function msg(text){$("status").textContent=text||""}
function expired(deadline){return deadline>0&&Date.now()/1000>=deadline}
function render(s){state=s;$("connection").textContent=s.connected?"已连接管理端":"等待管理端连接…";$("identity").textContent=[s.label,s.name].filter(Boolean).join(" · ")||"学生操作";
const si=s.signIn&&!expired(s.signIn.deadline);$("signin").classList.toggle("hidden",!si);if(si&&signId!==s.signIn.session){signId=s.signIn.session;$("signinButton").onclick=async()=>{try{await api("/api/signin",{session:signId});msg("签到已提交")}catch(e){msg(e.message)}}}
const p=s.poll&&!expired(s.poll.deadline);$("poll").classList.toggle("hidden",!p);if(p){$("pollQuestion").textContent=s.poll.question;$("pollDeadline").textContent=s.poll.deadline?"请在截止前选择":"请选择一个选项";if(pollId!==s.poll.session){pollId=s.poll.session;const box=$("pollOptions");box.replaceChildren(...s.poll.options.map(option=>{const b=document.createElement("button");b.className="option";b.textContent=option;b.onclick=async()=>{try{await api("/api/poll",{session:pollId,option});msg("投票已提交");box.querySelectorAll("button").forEach(x=>x.disabled=true)}catch(e){msg(e.message)}};return b}))}}
const hasPrompt=!!s.demo, running=s.demoRunning;$("demo").classList.toggle("hidden",!hasPrompt&&!running);$("demoAccept").classList.toggle("hidden",!hasPrompt);$("demoDecline").classList.toggle("hidden",!hasPrompt);$("demoStop").classList.toggle("hidden",!running);const recipients=s.demo?(s.demo.targetLabels||[]).join("、")||(s.demo.targetIds||[]).join("、"):"";$("demoText").textContent=running?"你的屏幕正在以低帧率只读分享。":"老师请求你向以下接收设备演示屏幕："+(recipients||"接收设备")+"。只有你同意后才会开始。";if(hasPrompt&&demoId!==s.demo.session){demoId=s.demo.session;$("demoAccept").onclick=async()=>{try{await api("/api/demo",{session:demoId,accept:true});msg("已同意演示，正在连接接收端")}catch(e){msg(e.message)}};$("demoDecline").onclick=async()=>{try{await api("/api/demo",{session:demoId,accept:false});msg("已拒绝演示请求")}catch(e){msg(e.message)}}}
$("helpButton").textContent=s.helpRaised?"已举手，等待老师":"举手求助";$("helpButton").disabled=s.helpRaised||!s.connected;
}
async function refresh(){try{render(await api("/api/state"))}catch(e){$("connection").textContent="本机页面连接中断"}}
$("helpButton").onclick=async()=>{try{await api("/api/help",{});msg("已通知老师");await refresh()}catch(e){msg(e.message)}};
$("demoStop").onclick=async()=>{try{await api("/api/demo/stop",{});msg("已请求停止演示");await refresh()}catch(e){msg(e.message)}};
$("file").onchange=async e=>{const f=e.target.files[0];if(!f)return;if(f.size>134217728){msg("文件超过 128 MiB");e.target.value="";return}const data=new FormData();data.append("file",f);msg("正在提交…");try{const r=await fetch("/api/upload",{method:"POST",headers:{"X-Lab-Token":token},body:data});if(!r.ok)throw new Error(await r.text());msg("文件已校验并送达老师")}catch(err){msg(err.message)}finally{e.target.value=""}};
refresh();setInterval(refresh,1000);
</script></body></html>`
