package publisher

import (
	"fmt"
)

// PushData 推送数据到流
func (s *Stream) PushData(data []byte) {
	// 检查流是否运行
	if !s.running {
		return
	}

	// 忽略空数据
	if len(data) == 0 {
		return
	}
	
	// 将数据放入缓冲区
	select {
	case s.buffer <- data:
		// 添加调试信息
		fmt.Printf("Stream %s received %d bytes of data\n", s.id, len(data))
	default:
		// 缓冲区满时丢弃数据
		fmt.Printf("Stream %s buffer full, dropping %d bytes of data\n", s.id, len(data))
	}
}