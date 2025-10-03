package publisher

import (
	"fmt"
)

// PushData 推送数据到流
func (s *Stream) PushData(data []byte) {
	if !s.running {
		return
	}
	
	// 将数据放入缓冲区
	select {
	case s.buffer <- data:
	default:
		// 缓冲区满时丢弃数据
		fmt.Printf("Stream %s buffer full, dropping data\n", s.id)
	}
}