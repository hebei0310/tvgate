package publisher

import (
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/qist/tvgate/logger"
)

// PublisherManager 管理多个流推流器
type PublisherManager struct {
	// 推流器配置
	config *PublisherConfig
	
	// 流推流器管理
	publishers map[string]*StreamPublisher
	mutex      sync.RWMutex
	
	// 运行状态
	running bool
	
	// 配置文件路径
	configPath string
}

// NewPublisherManager 创建一个新的推流管理器
func NewPublisherManager(config *PublisherConfig, configPath string) *PublisherManager {
	return &PublisherManager{
		config:     config,
		publishers: make(map[string]*StreamPublisher),
		configPath: configPath,
	}
}

// Start 启动推流管理器
func (pm *PublisherManager) Start() error {
	pm.running = true
	return nil
}

// Stop 停止推流管理器
func (pm *PublisherManager) Stop() {
	if !pm.running {
		return
	}
	
	pm.running = false
	
	// 停止所有推流器
	pm.mutex.Lock()
	defer pm.mutex.Unlock()
	
	for _, publisher := range pm.publishers {
		publisher.Stop()
	}
}

// GetPublisher 获取指定ID的推流器
func (pm *PublisherManager) GetPublisher(id string) *StreamPublisher {
	pm.mutex.RLock()
	defer pm.mutex.RUnlock()
	
	return pm.publishers[id]
}

// AddPublisher 添加推流器
func (pm *PublisherManager) AddPublisher(id string, config *StreamItemConfig, r *http.Request) (*StreamPublisher, error) {
	pm.mutex.Lock()
	defer pm.mutex.Unlock()
	
	// 检查推流器是否已存在
	if existingPublisher, exists := pm.publishers[id]; exists {
		return existingPublisher, nil
	}
	
	// 创建新的推流器
	publisher := NewStreamPublisher(&PublisherConfig{
		Path:   pm.config.Path,
		Streams: make(map[string]*StreamItemConfig),
	}, pm.configPath)
	
	// 启动推流器
	if err := publisher.Start(); err != nil {
		return nil, fmt.Errorf("启动推流器失败: %v", err)
	}
	
	// 添加到管理器中
	pm.publishers[id] = publisher
	
	// 添加流到推流器
	_, err := publisher.AddStream(id, config, r)
	if err != nil {
		return nil, fmt.Errorf("添加流失败: %v", err)
	}
	
	return publisher, nil
}

// RemovePublisher 移除推流器
func (pm *PublisherManager) RemovePublisher(id string) {
	pm.mutex.Lock()
	defer pm.mutex.Unlock()
	
	if publisher, exists := pm.publishers[id]; exists {
		publisher.Stop()
		delete(pm.publishers, id)
	}
}

// ListPublishers 列出所有推流器
func (pm *PublisherManager) ListPublishers() []*StreamPublisher {
	pm.mutex.RLock()
	defer pm.mutex.RUnlock()
	
	publishers := make([]*StreamPublisher, 0, len(pm.publishers))
	for _, publisher := range pm.publishers {
		publishers = append(publishers, publisher)
	}
	
	return publishers
}

// CheckStreamKeyExpiration 检查所有流密钥过期
func (pm *PublisherManager) CheckStreamKeyExpiration() {
	pm.mutex.RLock()
	publishers := make([]*StreamPublisher, 0, len(pm.publishers))
	for _, publisher := range pm.publishers {
		publishers = append(publishers, publisher)
	}
	pm.mutex.RUnlock()
	
	for _, publisher := range publishers {
		publisher.checkStreamKeyExpiration()
	}
}

// StartExpirationChecker 启动过期检查器
func (pm *PublisherManager) StartExpirationChecker() {
	go func() {
		ticker := time.NewTicker(1 * time.Minute)
		defer ticker.Stop()
		
		for pm.running {
			select {
			case <-ticker.C:
				pm.CheckStreamKeyExpiration()
			case <-time.After(1 * time.Second):
				// 防止阻塞
			}
		}
	}()
	
	logger.LogPrintf("PublisherManager expiration checker started")
}