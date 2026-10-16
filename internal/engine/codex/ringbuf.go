package codex

import "sync"

// ringBuffer 保留最近的一段输出，用于诊断。
//
// helper 的 stderr 可能持续输出，不能无限累积 —— 只留尾部
// （出错前的上下文通常才是关键的）。
type ringBuffer struct {
	mu   sync.Mutex
	data []byte
	max  int
}

func newRingBuffer(max int) *ringBuffer {
	if max <= 0 {
		max = 8 << 10
	}
	return &ringBuffer{max: max}
}

func (r *ringBuffer) Write(p []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.data = append(r.data, p...)
	if len(r.data) > r.max {
		r.data = r.data[len(r.data)-r.max:]
	}
}

func (r *ringBuffer) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return string(r.data)
}
