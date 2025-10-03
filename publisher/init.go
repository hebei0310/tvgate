package publisher

import (
	"fmt"
)

// Init 初始化推流器
func Init(config *PublisherConfig) (*StreamPublisher, error) {
	// 检查是否有启用的流
	hasEnabledStreams := false
	for _, stream := range config.Streams {
		if stream.Enabled {
			hasEnabledStreams = true
			break
		}
	}
	
	if !hasEnabledStreams {
		return nil, nil
	}

	// 传递配置文件路径给NewStreamPublisher
	publisher := NewStreamPublisher(config, "") // configPath需要从外部传入
	
	if err := publisher.Start(); err != nil {
		return nil, fmt.Errorf("启动推流器失败: %w", err)
	}

	return publisher, nil
}