package lan

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// 端到端：server + agent 走真实 TCP/握手/心跳/指令。
func TestRoundTrip(t *testing.T) {
	id, err := GenIdentity()
	if err != nil {
		t.Fatal(err)
	}
	srv := NewServer(id, 47893) // 测试端口，避开默认
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := srv.Run(ctx); err != nil {
		t.Fatal(err)
	}

	a := NewAgent("pc-01", "TEST-PC01")
	a.Label = "A-01"
	a.IP = "127.0.0.1"
	a.MAC = "00:11:22:33:44:55"
	a.PinnedPub = srv.ID.Pub()
	a.DirectAddr = "127.0.0.1:47893"
	go a.Run(ctx)

	// 等上线
	deadline := time.Now().Add(5 * time.Second)
	for {
		d, ok := srv.GetDevice("pc-01")
		if ok && d.Online {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("agent 未上线")
		}
		time.Sleep(50 * time.Millisecond)
	}

	// 下发 helper.set false
	ack, err := srv.SendCommand("pc-01", OpSetHelper, SetHelperArgs{On: false})
	if err != nil {
		t.Fatal(err)
	}
	if !ack.OK {
		t.Fatalf("ack 失败: %s", ack.Err)
	}
	// 等 status 推回
	time.Sleep(100 * time.Millisecond)
	d, _ := srv.GetDevice("pc-01")
	if d.HelperOn {
		t.Fatal("注册表未更新 helperOn=false")
	}

	// 下发 features.set
	keys := []string{"usb_storage", "wallpaper"}
	if _, err := srv.SendCommand("pc-01", OpSetFeatures, SetFeaturesArgs{Keys: keys}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	d, _ = srv.GetDevice("pc-01")
	if fmt.Sprint(d.Features) != fmt.Sprint(keys) {
		t.Fatalf("features 不符: %v", d.Features)
	}

	// 篡改签名应失败：用错误公钥握手
	bad := NewAgent("pc-evil", "EVIL")
	bad.PinnedPub = mustGenOtherPub(t)
	bad.DirectAddr = "127.0.0.1:47893"
	// clientHandshake 会因签名不匹配失败 → session 不建立，server 无设备
	if err := bad.session(ctx, bad.DirectAddr, bad.PinnedPub); err == nil {
		t.Fatal("伪造公钥的握手竟成功了")
	}
}

func mustGenOtherPub(t *testing.T) []byte {
	t.Helper()
	other, err := GenIdentity()
	if err != nil {
		t.Fatal(err)
	}
	return other.Pub()
}
