package lan

import (
	"context"
	"encoding/base64"
	"time"
)

// frameWorker keeps only the newest frame for a client. A slow client can
// delay its own stream, but it cannot build an unbounded backlog or block the
// other recipients of a broadcast.
type frameWorker struct {
	cc     *clientConn
	frames chan []byte
	cancel context.CancelFunc
}

// QueueCastFrame sends a frame to the selected clients through one-slot queues.
func (s *Server) QueueCastFrame(ids []string, jpeg []byte) int {
	if len(jpeg) == 0 || len(jpeg) > MaxCastJPEG {
		return 0
	}
	s.mu.Lock()
	targets := make([]*clientConn, 0, len(ids))
	for _, id := range ids {
		if cc := s.sessions[id]; cc != nil {
			targets = append(targets, cc)
		}
	}
	s.mu.Unlock()

	queued := 0
	for _, cc := range targets {
		worker := s.frameWorker(cc)
		select {
		case worker.frames <- jpeg:
			queued++
		default:
			select {
			case <-worker.frames:
			default:
			}
			select {
			case worker.frames <- jpeg:
				queued++
			default:
			}
		}
	}
	return queued
}

func (s *Server) frameWorker(cc *clientConn) *frameWorker {
	s.mu.Lock()
	if worker := s.frameWorkers[cc.devID]; worker != nil && worker.cc == cc {
		s.mu.Unlock()
		return worker
	} else if worker != nil {
		worker.cancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	worker := &frameWorker{cc: cc, frames: make(chan []byte, 1), cancel: cancel}
	s.frameWorkers[cc.devID] = worker
	s.mu.Unlock()
	go func() {
		defer func() {
			// 退出时清掉自己在 map 里的条目，否则后续 QueueCastFrame
			// 会拿到已取消的 worker 静默丢帧。
			s.mu.Lock()
			if s.frameWorkers[cc.devID] == worker {
				delete(s.frameWorkers, cc.devID)
			}
			s.mu.Unlock()
		}()
		for {
			select {
			case <-ctx.Done():
				return
			case frame := <-worker.frames:
				msg := Msg{Type: MsgCast, Cast: &CastMsg{JPEG: base64.StdEncoding.EncodeToString(frame)}}
				if err := cc.sess.sendWithTimeout(msg, 3*time.Second); err != nil {
					worker.cancel()
					_ = cc.sess.conn.Close()
					return
				}
			}
		}
	}()
	return worker
}

// StopCastFrames cancels queued frame workers for the selected devices.
func (s *Server) StopCastFrames(ids []string) {
	s.mu.Lock()
	for _, id := range ids {
		if worker := s.frameWorkers[id]; worker != nil {
			worker.cancel()
			delete(s.frameWorkers, id)
		}
	}
	s.mu.Unlock()
}
