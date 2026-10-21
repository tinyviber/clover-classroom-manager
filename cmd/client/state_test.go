package main

import (
	"os"
	"path/filepath"
	"testing"
)

// 策略持久化：可变状态写进用户可写的 state.json，重启后恢复；
// 路径不可写时保存必须报错（经 PersistState 回调转成设备告警），
// 而不是静默丢弃——安装目录被 ACL 收成只读时就是这条路径。
func TestAgentStateRoundTrip(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "LabHelper", "state.json")

	if st := loadAgentState(p); st != nil {
		t.Fatal("不存在的 state.json 应返回 nil")
	}
	if err := saveAgentState(p, true, []string{"guard_offline", "lock_taskmgr"}); err != nil {
		t.Fatal(err)
	}
	st := loadAgentState(p)
	if st == nil || st.HelperOn == nil || !*st.HelperOn {
		t.Fatal("helperOn 未持久化")
	}
	if len(st.Features) != 2 || st.Features[0] != "guard_offline" {
		t.Fatalf("features 未持久化: %v", st.Features)
	}
	// 覆盖写
	if err := saveAgentState(p, false, nil); err != nil {
		t.Fatal(err)
	}
	st = loadAgentState(p)
	if st == nil || st.HelperOn == nil || *st.HelperOn {
		t.Fatal("覆盖写未生效")
	}
}

func TestAgentStateWriteFailure(t *testing.T) {
	dir := t.TempDir()
	// 目录只读 → 保存必须返回错误（调用方据此上报告警）。
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Skipf("chmod 不可用: %v", err)
	}
	defer os.Chmod(dir, 0o755)
	err := saveAgentState(filepath.Join(dir, "state.json"), true, nil)
	if err == nil {
		t.Fatal("只读目录下保存竟成功（root 运行?）")
	}
}
