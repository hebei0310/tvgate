package publisher

import (
	"fmt"
)

// Init 初始化推流器
func Init(config *Config) (*StreamPublisher, error) {
	if !config.Enabled {
		return nil, nil
	}

	// 传递配置文件路径给NewStreamPublisher
	publisher := NewStreamPublisher(config, config.ConfigPath)
	
	if err := publisher.Start(); err != nil {
		return nil, fmt.Errorf("启动推流器失败: %w", err)
	}

	return publisher, nil
}