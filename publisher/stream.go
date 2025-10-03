package publisher

import (
	"fmt"
	"gopkg.in/yaml.v3"
	"net/http"
	"os"
	"time"
	
	"github.com/qist/tvgate/logger"
)

// Start 启动推流服务
func (sp *StreamPublisher) Start() error {
	sp.running = true
	
	// 启动过期检查器
	sp.startExpirationChecker()
	
	return nil
}

// Stop 停止推流服务
func (sp *StreamPublisher) Stop() {
	if !sp.running {
		return
	}
	
	sp.running = false
	close(sp.done)
	
	// 停止过期检查器
	if sp.expirationChecker != nil {
		sp.expirationChecker.Stop()
	}
	
	// 停止所有流
	sp.streamsMu.Lock()
	defer sp.streamsMu.Unlock()
	
	for _, stream := range sp.streams {
		stream.Stop()
	}
}

// startExpirationChecker 启动过期检查器
func (sp *StreamPublisher) startExpirationChecker() {
	sp.expirationChecker = time.NewTicker(1 * time.Minute)
	go func() {
		for {
			select {
			case <-sp.expirationChecker.C:
				sp.checkStreamKeyExpiration()
			case <-sp.done:
				return
			}
		}
	}()
}

// checkStreamKeyExpiration 检查流密钥过期
func (sp *StreamPublisher) checkStreamKeyExpiration() {
	sp.streamsMu.RLock()
	streams := make([]*Stream, 0, len(sp.streams))
	for _, stream := range sp.streams {
		streams = append(streams, stream)
	}
	sp.streamsMu.RUnlock()
	
	for _, stream := range streams {
		stream.CheckStreamKeyExpiration()
	}
}

// AddStream 添加流
func (sp *StreamPublisher) AddStream(id string, config *StreamItemConfig, r *http.Request) (*Stream, error) {
	sp.streamsMu.Lock()
	defer sp.streamsMu.Unlock()
	
	// 检查流是否已存在
	if existingStream, exists := sp.streams[id]; exists {
		return existingStream, nil
	}
	
	// 检查协议类型
	switch config.Protocol {
	case "go":
		// 使用Go原生方式处理流
		fmt.Printf("使用Go原生方式处理流: %s\n", id)
	case "ffmpeg":
		// 使用FFmpeg处理流（需要实现）
		fmt.Printf("使用FFmpeg处理流: %s\n", id)
		// TODO: 实现FFmpeg处理逻辑
	default:
		return nil, fmt.Errorf("不支持的协议类型: %s", config.Protocol)
	}
	
	// 生成或获取流密钥（直接在config中生成）
	config.GenerateStreamKey()
	
	// 创建新流
	stream := &Stream{
		id:        id,
		config:    config,
		clients:   make(map[string]*Client),
		buffer:    make(chan []byte, config.BufferSize),
		running:   false,
		done:      make(chan struct{}),
		publisher: sp,
	}
	
	// 获取接收方配置
	receivers := config.Stream.Receivers
	
	// 根据模式添加客户端
	switch config.Stream.Mode {
	case "primary-backup":
		// 添加主接收方
		if primary, exists := receivers["primary"]; exists && primary.PushURL != "" {
			client := &Client{
				id:        fmt.Sprintf("%s-primary", id),
				pushURL:   primary.PushURL,
				playURLs:  primary.PlayURLs,
				isPrimary: true,
			}
			stream.AddClient(client)
		}
		
		// 添加备接收方
		if backup, exists := receivers["backup"]; exists && backup.PushURL != "" {
			client := &Client{
				id:        fmt.Sprintf("%s-backup", id),
				pushURL:   backup.PushURL,
				playURLs:  backup.PlayURLs,
				isPrimary: false,
			}
			stream.AddClient(client)
		}
		
	case "all":
		// 添加所有接收方
		for name, receiver := range receivers {
			if receiver.PushURL != "" {
				client := &Client{
					id:        fmt.Sprintf("%s-%s", id, name),
					pushURL:   receiver.PushURL,
					playURLs:  receiver.PlayURLs,
					isPrimary: name == "primary",
				}
				stream.AddClient(client)
			}
		}
	}
	
	// 保存流密钥到配置文件中
	if err := sp.SaveStreamKeyToConfig(id, config); err != nil {
		logger.LogPrintf("保存流密钥到配置文件失败: %v", err)
	}
	
	// 保存流到映射中
	sp.streams[id] = stream
	
	// 启动流
	stream.Start()
	
	return stream, nil
}

// GetStream 获取流
func (sp *StreamPublisher) GetStream(id string) *Stream {
	sp.streamsMu.RLock()
	defer sp.streamsMu.RUnlock()
	
	if stream, exists := sp.streams[id]; exists {
		return stream
	}
	return nil
}

// RemoveStream 移除流
func (sp *StreamPublisher) RemoveStream(id string) {
	sp.streamsMu.Lock()
	defer sp.streamsMu.Unlock()
	
	if stream, exists := sp.streams[id]; exists {
		stream.Stop()
		delete(sp.streams, id)
	}
}

// ListStreams 列出所有流
func (sp *StreamPublisher) ListStreams() []*Stream {
	sp.streamsMu.RLock()
	defer sp.streamsMu.RUnlock()
	
	streams := make([]*Stream, 0, len(sp.streams))
	for _, stream := range sp.streams {
		streams = append(streams, stream)
	}
	
	return streams
}

// SaveStreamKeyToConfig 保存流密钥到配置文件
func (sp *StreamPublisher) SaveStreamKeyToConfig(id string, config *StreamItemConfig) error {
	// 如果没有配置文件路径，则不保存
	if sp.configPath == "" {
		return nil
	}
	
	// 读取原始配置文件
	data, err := os.ReadFile(sp.configPath)
	if err != nil {
		return fmt.Errorf("读取配置文件失败: %v", err)
	}
	
	// 解析YAML
	var rootConfig map[string]interface{}
	if err := yaml.Unmarshal(data, &rootConfig); err != nil {
		return fmt.Errorf("解析配置文件失败: %v", err)
	}
	
	// 更新publisher配置
	if publisher, ok := rootConfig["publisher"].(map[string]interface{}); ok {
		// 更新指定流的streamkey
		if streams, ok := publisher[id].(map[string]interface{}); ok {
			streams["streamkey"] = map[string]interface{}{
				"type":       config.StreamKey.Type,
				"value":      config.StreamKey.Value,
				"length":     config.StreamKey.Length,
				"expiration": config.StreamKey.Expiration,
				"generated":  config.StreamKey.Generated,
			}
		}
	}
	
	// 将更新后的配置写回文件
	updatedData, err := yaml.Marshal(rootConfig)
	if err != nil {
		return fmt.Errorf("序列化配置失败: %v", err)
	}
	
	if err := os.WriteFile(sp.configPath, updatedData, 0644); err != nil {
		return fmt.Errorf("写入配置文件失败: %v", err)
	}
	
	return nil
}