package castview

import (
	"bytes"
	"io"
	"testing"
)

// 进程间消息帧：写读往返一致 + 超长按拒。
func TestMsgRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	if err := writeMsg(&buf, opShow, []byte("fullscreen")); err != nil {
		t.Fatal(err)
	}
	if err := writeMsg(&buf, opClose, nil); err != nil {
		t.Fatal(err)
	}
	op, p, err := readMsg(&buf)
	if err != nil || op != opShow || string(p) != "fullscreen" {
		t.Fatalf("op=%d p=%q err=%v", op, p, err)
	}
	op, p, err = readMsg(&buf)
	if err != nil || op != opClose || len(p) != 0 {
		t.Fatalf("op=%d p=%q err=%v", op, p, err)
	}
	if _, _, err := readMsg(&buf); err != io.EOF {
		t.Fatalf("读完应为 EOF: %v", err)
	}

	// 长度头造假：超限直接拒
	var bad bytes.Buffer
	bad.Write([]byte{0xFF, 0xFF, 0xFF, 0xFF, 0x01})
	if _, _, err := readMsg(&bad); err == nil {
		t.Fatal("超长消息未被拒绝")
	}
}
