package publisher

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"net/http"
	"io"
	"time"
	"github.com/qist/tvgate/logger"
	"strings"
	"gopkg.in/yaml.v3"
	"os"
	// "net/url"
)

// StreamPublisher 流推流器结构
type StreamPublisher struct {
	// 推流配置
	config *PublisherConfig
	
	// 流管理
	streams   map[string]*Stream
	streamsMu sync.RWMutex
	
	// 运行状态
	running bool
	
	// HTTP客户端用于拉流
	httpClient *http.Client
	
	// 定时检查streamkey过期
	expirationChecker *time.Ticker
	done              chan struct{}
	
	// 配置文件路径
	configPath string
}

// Stream 流结构
type Stream struct {
	// 流ID
	id string
	
	// 流配置
	config *StreamItemConfig
	
	// 流密钥
	streamKey string
	
	// 客户端管理
	clients   map[string]*Client
	clientsMu sync.RWMutex
	
	// 数据缓冲
	buffer chan []byte
	
	// 运行状态
	running bool
	done   chan struct{}
	
	// 父级推流器引用
	publisher *StreamPublisher
	
	// 本地播放URL
	localPlayURLs PlayURLs
}

// Client 客户端结构
type Client struct {
	// 客户端ID
	id string
	
	// 推流地址
	pushURL string
	
	// 播放地址
	playURLs PlayURLs
	
	// 是否是主推流
	isPrimary bool
}

// NewStreamPublisher 创建一个新的流推流器
func NewStreamPublisher(config *PublisherConfig, configPath string) *StreamPublisher {
	return &StreamPublisher{
		config:  config,
		streams: make(map[string]*Stream),
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		done: make(chan struct{}),
		configPath: configPath, // 保存配置文件路径
	}
}

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
	
	// 生成或获取流密钥
	streamKey := config.GenerateStreamKey()
	
	// 创建新流
	stream := &Stream{
		id:        id,
		config:    config,
		streamKey: streamKey,
		clients:   make(map[string]*Client),
		buffer:    make(chan []byte, config.BufferSize),
		running:   false,
		done:      make(chan struct{}),
		publisher: sp,
	}
	
	// 构建本地播放URL
	localFLVURL := ""
	localHLSURL := ""
	
	// 处理本地播放URL
	if config.Stream.LocalPlayURLs.FLV != "" {
		if strings.HasSuffix(config.Stream.LocalPlayURLs.FLV, "/") {
			localFLVURL = fmt.Sprintf("%s%s.flv", config.Stream.LocalPlayURLs.FLV, streamKey)
		} else {
			localFLVURL = config.Stream.LocalPlayURLs.FLV
		}
	} else {
		// 自动生成本地播放URL
		if r != nil {
			scheme := "http"
			if r.TLS != nil {
				scheme = "https"
			}
			host := r.Host
			localFLVURL = fmt.Sprintf("%s://%s%s/play/%s.flv", scheme, host, sp.config.Path, streamKey)
		}
	}
	
	if config.Stream.LocalPlayURLs.HLS != "" {
		if strings.HasSuffix(config.Stream.LocalPlayURLs.HLS, "/") {
			localHLSURL = fmt.Sprintf("%s%s.m3u8", config.Stream.LocalPlayURLs.HLS, streamKey)
		} else {
			localHLSURL = config.Stream.LocalPlayURLs.HLS
		}
	} else {
		// 自动生成本地播放URL
		if r != nil {
			scheme := "http"
			if r.TLS != nil {
				scheme = "https"
			}
			host := r.Host
			localHLSURL = fmt.Sprintf("%s://%s%s/play/%s.m3u8", scheme, host, sp.config.Path, streamKey)
		}
	}
	
	stream.localPlayURLs = PlayURLs{
		FLV: localFLVURL,
		HLS: localHLSURL,
	}
	
	// 根据模式添加客户端
	receivers := config.Stream.Receivers
	switch config.Stream.Mode {
	case "primary-backup":
		// 添加主推流客户端
		if primary, exists := receivers["primary"]; exists && primary.PushURL != "" {
			client := &Client{
				id:        fmt.Sprintf("%s-primary", id),
				pushURL:   primary.PushURL,
				playURLs:  primary.PlayURLs,
				isPrimary: true,
			}
			stream.AddClient(client)
		}
		
		// 添加备用推流客户端
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

// GetConfig 获取配置
func (sp *StreamPublisher) GetConfig() *PublisherConfig {
	return sp.config
}

// SaveStreamKeyToConfig 将生成的streamkey保存到配置文件中
func (sp *StreamPublisher) SaveStreamKeyToConfig(streamID string, config *StreamItemConfig) error {
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
		if streamConfig, ok := publisher[streamID].(map[string]interface{}); ok {
			// 更新streamkey的值、生成时间和过期时间
			if streamKey, ok := streamConfig["streamkey"].(map[string]interface{}); ok {
				streamKey["value"] = config.StreamKey.Value
				streamKey["generated"] = config.StreamKey.Generated.Format(time.RFC3339)
				streamKey["expiration"] = config.StreamKey.Expiration
			}
			
			// 更新stream配置中的URL
			if stream, ok := streamConfig["stream"].(map[string]interface{}); ok {
				// 更新local_play_urls
				if localPlayURLs, ok := stream["local_play_urls"].(map[string]interface{}); ok {
					if config.Stream.LocalPlayURLs.FLV != "" {
						localPlayURLs["flv"] = config.Stream.LocalPlayURLs.FLV
					}
					if config.Stream.LocalPlayURLs.HLS != "" {
						localPlayURLs["hls"] = config.Stream.LocalPlayURLs.HLS
					}
				}
				
				// 更新receivers中的URL
				if receivers, ok := stream["receivers"].(map[string]interface{}); ok {
					for receiverName, receiverData := range config.Stream.Receivers {
						// 确保receiver存在
						if _, exists := receivers[receiverName]; !exists {
							receivers[receiverName] = map[string]interface{}{
								"push_url":  "",
								"play_urls": map[string]interface{}{},
							}
						}
						
						if receiver, ok := receivers[receiverName].(map[string]interface{}); ok {
							// 更新push_url
							if receiverData.PushURL != "" {
								receiver["push_url"] = receiverData.PushURL
							} else if receiverData.PushURL == "" && receiver["push_url"] == nil {
								receiver["push_url"] = ""
							}
							
							// 更新play_urls
							if _, exists := receiver["play_urls"]; !exists {
								receiver["play_urls"] = map[string]interface{}{}
							}
							
							if playURLs, ok := receiver["play_urls"].(map[string]interface{}); ok {
								// 确保playURLs对象存在
								if playURLs == nil {
									playURLs = map[string]interface{}{}
									receiver["play_urls"] = playURLs
								}
								
								if receiverData.PlayURLs.FLV != "" {
									playURLs["flv"] = receiverData.PlayURLs.FLV
								}
								if receiverData.PlayURLs.HLS != "" {
									playURLs["hls"] = receiverData.PlayURLs.HLS
								}
							}
						}
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
			
			fmt.Printf("Stream %s configuration saved to config file\n", streamID)
			return nil
		}
	}
	
	return fmt.Errorf("无法在配置文件中找到stream %s 的配置", streamID)
}

// Start 启动流
func (s *Stream) Start() {
	// 启动数据处理协程
	go s.processData()
	
	// 启动拉流协程
	go s.pullStream()
	
	fmt.Printf("Stream %s started\n", s.id)
}

// Stop 停止流
func (s *Stream) Stop() {
	s.running = false
	close(s.done)
	close(s.buffer)
	
	// 断开所有客户端
	s.clientsMu.Lock()
	for _, client := range s.clients {
		s.removeClient(client)
	}
	s.clientsMu.Unlock()
	
	fmt.Printf("Stream %s stopped\n", s.id)
}

// AddClient 添加客户端
func (s *Stream) AddClient(client *Client) {
	s.clientsMu.Lock()
	defer s.clientsMu.Unlock()
	
	s.clients[client.id] = client
	fmt.Printf("Client %s added to stream %s\n", client.id, s.id)
}

// RemoveClient 移除客户端
func (s *Stream) RemoveClient(clientID string) {
	s.clientsMu.Lock()
	defer s.clientsMu.Unlock()
	
	if client, exists := s.clients[clientID]; exists {
		s.removeClient(client)
		delete(s.clients, clientID)
		fmt.Printf("Client %s removed from stream %s\n", clientID, s.id)
	}
}

// removeClient 私有方法，移除客户端
func (s *Stream) removeClient(client *Client) {
	// 实际断开客户端连接的逻辑
	fmt.Printf("Client %s disconnected from stream %s\n", client.id, s.id)
}

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

// pullStream 从源地址拉取流
func (s *Stream) pullStream() {
	if s.config.Stream.Source.URL == "" {
		logger.LogPrintf("Stream %s has no source URL configured", s.id)
		return
	}
	
	// 根据源类型选择拉流方式
	switch s.config.Stream.Source.Type {
	case "rtsp":
		s.pullRTSPStream()
	case "http", "https":
		s.pullHTTPStream()
	default:
		logger.LogPrintf("Unsupported stream source type: %s", s.config.Stream.Source.Type)
	}
}

// pullRTSPStream 拉取RTSP流
func (s *Stream) pullRTSPStream() {
	// RTSP流拉取逻辑
	logger.LogPrintf("Pulling RTSP stream from %s", s.config.Stream.Source.URL)
	
	// 这里应该实现RTSP流的拉取逻辑
	// 可以使用现有的RTSP库或自定义实现
}

// pullHTTPStream 拉取HTTP流（包括HLS）
func (s *Stream) pullHTTPStream() {
	// HTTP流拉取逻辑
	logger.LogPrintf("Pulling HTTP stream from %s", s.config.Stream.Source.URL)
	
	// 创建HTTP请求
	req, err := http.NewRequest("GET", s.config.Stream.Source.URL, nil)
	if err != nil {
		logger.LogPrintf("Failed to create HTTP request for stream %s: %v", s.id, err)
		return
	}
	
	// 添加请求头
	for key, value := range s.config.Stream.Source.Headers {
		req.Header.Set(key, value)
	}
	
	// 发送HTTP请求
	resp, err := s.publisher.httpClient.Do(req)
	if err != nil {
		logger.LogPrintf("Failed to pull HTTP stream %s: %v", s.id, err)
		return
	}
	defer resp.Body.Close()
	
	// 检查响应状态码
	if resp.StatusCode != http.StatusOK {
		logger.LogPrintf("HTTP stream %s returned status code: %d", s.id, resp.StatusCode)
		return
	}
	
	// 读取并转发数据
	buffer := make([]byte, 4096)
	for {
		select {
		case <-s.done:
			return
		default:
			n, err := resp.Body.Read(buffer)
			if err != nil && err != io.EOF {
				logger.LogPrintf("Failed to read HTTP stream %s: %v", s.id, err)
				return
			}
			
			if n > 0 {
				// 将数据推送到缓冲区
				s.PushData(buffer[:n])
			}
			
			if err == io.EOF {
				// 流结束，尝试重新连接
				logger.LogPrintf("HTTP stream %s ended, reconnecting...", s.id)
				time.Sleep(1 * time.Second)
				go s.pullHTTPStream()
				return
			}
		}
	}
}

// processData 处理流数据
func (s *Stream) processData() {
	for {
		select {
		case data := <-s.buffer:
			// 将数据转发给所有客户端
			s.clientsMu.RLock()
			for _, client := range s.clients {
				// 这里应该实现向客户端推送数据的逻辑
				_ = client
				_ = data
				// 示例：client.PushData(data)
			}
			s.clientsMu.RUnlock()
		case <-s.done:
			return
		}
	}
}

// GetClients 获取客户端列表
func (s *Stream) GetClients() []*Client {
	s.clientsMu.RLock()
	defer s.clientsMu.RUnlock()
	
	clients := make([]*Client, 0, len(s.clients))
	for _, client := range s.clients {
		clients = append(clients, client)
	}
	return clients
}

// CheckStreamKeyExpiration 检查流密钥是否过期
func (s *Stream) CheckStreamKeyExpiration() {
	if s.config.IsStreamKeyExpired() {
		// 生成新的流密钥
		newKey := s.config.GenerateStreamKey()
		fmt.Printf("Stream %s key expired, generated new key: %s\n", s.id, newKey)
		
		// 更新所有相关URL中的流密钥
		updatedConfig := s.config.ReplacePlaceholders(newKey)
		
		// 保存更新后的配置
		if s.publisher != nil {
			if err := s.publisher.SaveStreamKeyToConfig(s.id, updatedConfig); err != nil {
				logger.LogPrintf("Failed to save updated stream key for %s: %v", s.id, err)
			}
		}
	}
}

// HandleLocalPlay 处理本地播放请求
func (s *Stream) HandleLocalPlay(w http.ResponseWriter, r *http.Request) {
	// 这里应该实现本地播放逻辑
	// 可以是FLV或HLS流的转发
	fmt.Fprintf(w, "Local play for stream %s", s.id)
}

// GenerateStreamKey 生成或获取流密钥
func (c *StreamItemConfig) GenerateStreamKey() string {
	// 检查是否为固定密钥
	if c.StreamKey.Type == "fixed" {
		// 更新生成时间（即使密钥不变也要更新时间戳）
		c.StreamKey.Generated = time.Now()
		// 保存配置到文件
		// 注意：这里需要访问publisher来保存配置
		return c.StreamKey.Value
	}

	// 检查是否存在现有密钥
	if c.StreamKey.Value != "" {
		// 解析过期时间
		if c.StreamKey.Expiration != "" && c.StreamKey.expiration == 0 {
			if exp, err := time.ParseDuration(c.StreamKey.Expiration); err == nil {
				c.StreamKey.expiration = exp
			}
		}

		// 检查是否过期
		if c.StreamKey.expiration > 0 && !c.StreamKey.Generated.IsZero() {
			if time.Since(c.StreamKey.Generated) < c.StreamKey.expiration {
				// 密钥未过期，但为了确保配置一致性，更新生成时间
				c.StreamKey.Generated = time.Now()
				// 保存配置到文件
				return c.StreamKey.Value
			}
		} else {
			// 永不过期，但为了确保配置一致性，更新生成时间
			c.StreamKey.Generated = time.Now()
			// 保存配置到文件
			return c.StreamKey.Value
		}
	}

	// 生成随机密钥
	length := c.StreamKey.Length
	if length <= 0 {
		length = 16
	}

	bytes := make([]byte, length/2)
	if _, err := rand.Read(bytes); err != nil {
		// fallback to math/rand if crypto/rand fails
		for i := range bytes {
			bytes[i] = byte(time.Now().UnixNano() >> uint(i%8))
		}
	}
	
	key := hex.EncodeToString(bytes)
	c.StreamKey.Value = key
	c.StreamKey.Generated = time.Now()
	
	return key
}

// IsStreamKeyExpired 检查流密钥是否过期
func (c *StreamItemConfig) IsStreamKeyExpired() bool {
	// 固定密钥永不过期
	if c.StreamKey.Type == "fixed" {
		return false
	}

	// 没有设置过期时间，永不过期
	if c.StreamKey.Expiration == "0" {
		return false
	}

	// 解析过期时间
	if c.StreamKey.expiration == 0 {
		exp, err := time.ParseDuration(c.StreamKey.Expiration)
		if err != nil {
			return false // 解析失败则认为永不过期
		}
		c.StreamKey.expiration = exp
	}

	// 检查是否过期
	return !c.StreamKey.Generated.IsZero() && time.Since(c.StreamKey.Generated) >= c.StreamKey.expiration
}

// ReplacePlaceholders 替换配置中的旧流密钥
func (c *StreamItemConfig) ReplacePlaceholders(streamKey string) *StreamItemConfig {
	// 创建配置副本
	config := &StreamItemConfig{
		Protocol:   c.Protocol,
		BufferSize: c.BufferSize,
		Enabled:    c.Enabled,
		StreamKey: StreamKeyConfig{
			Type:       c.StreamKey.Type,
			Value:      streamKey, // 使用新的流密钥
			Length:     c.StreamKey.Length,
			Expiration: c.StreamKey.Expiration,
			expiration: c.StreamKey.expiration,
			Generated:  time.Now(), // 更新生成时间
		},
		Stream: StreamConfig{
			Source: SourceConfig{
				Type:      c.Stream.Source.Type,
				URL:       c.Stream.Source.URL,
				BackupURL: c.Stream.Source.BackupURL,
				Headers:   make(map[string]string),
			},
			LocalPlayURLs: PlayURLs{
				FLV: c.Stream.LocalPlayURLs.FLV,
				HLS: c.Stream.LocalPlayURLs.HLS,
			},
			Mode:      c.Stream.Mode,
			Receivers: make(map[string]ReceiverConfig),
		},
		ConfigPath: c.ConfigPath,
	}

	// 复制Headers
	for key, value := range c.Stream.Source.Headers {
		config.Stream.Source.Headers[key] = value
	}

	// 从接收方的推流URL中提取旧的流密钥
	oldStreamKey := ""
	for _, receiver := range c.Stream.Receivers {
		if receiver.PushURL != "" {
			oldStreamKey = extractKeyFromPushURL(receiver.PushURL)
			if oldStreamKey != "" {
				break
			}
		}
	}

	fmt.Printf("Extracted old stream key from push URLs: %s\n", oldStreamKey)
	fmt.Printf("Using new stream key: %s\n", streamKey)

	// 替换源URL中的旧流密钥
	if oldStreamKey != "" {
		config.Stream.Source.URL = strings.ReplaceAll(config.Stream.Source.URL, oldStreamKey, streamKey)
		config.Stream.Source.BackupURL = strings.ReplaceAll(config.Stream.Source.BackupURL, oldStreamKey, streamKey)
	}

	// 替换Headers中的旧流密钥
	for key, value := range config.Stream.Source.Headers {
		if oldStreamKey != "" {
			config.Stream.Source.Headers[key] = strings.ReplaceAll(value, oldStreamKey, streamKey)
		}
	}

	// 处理本地播放URL - 这里需要替换
	if oldStreamKey != "" {
		if config.Stream.LocalPlayURLs.FLV != "" {
			config.Stream.LocalPlayURLs.FLV = strings.ReplaceAll(config.Stream.LocalPlayURLs.FLV, oldStreamKey, streamKey)
		}
		if config.Stream.LocalPlayURLs.HLS != "" {
			config.Stream.LocalPlayURLs.HLS = strings.ReplaceAll(config.Stream.LocalPlayURLs.HLS, oldStreamKey, streamKey)
		}
	}

	fmt.Printf("Local play URLs replaced: FLV=%s, HLS=%s\n",
		config.Stream.LocalPlayURLs.FLV, config.Stream.LocalPlayURLs.HLS)

	// 替换接收方配置中的旧流密钥
	for name, receiver := range c.Stream.Receivers {
		newReceiver := ReceiverConfig{
			PushURL: receiver.PushURL,
			PlayURLs: PlayURLs{
				FLV: receiver.PlayURLs.FLV,
				HLS: receiver.PlayURLs.HLS,
			},
		}

		// 替换推流URL中的旧流密钥
		if oldStreamKey != "" && newReceiver.PushURL != "" {
			newReceiver.PushURL = strings.ReplaceAll(newReceiver.PushURL, oldStreamKey, streamKey)
		}

		// 替换播放URL中的旧流密钥
		if oldStreamKey != "" {
			if newReceiver.PlayURLs.FLV != "" {
				newReceiver.PlayURLs.FLV = strings.ReplaceAll(newReceiver.PlayURLs.FLV, oldStreamKey, streamKey)
			}
			if newReceiver.PlayURLs.HLS != "" {
				newReceiver.PlayURLs.HLS = strings.ReplaceAll(newReceiver.PlayURLs.HLS, oldStreamKey, streamKey)
			}
		}

		config.Stream.Receivers[name] = newReceiver

		fmt.Printf("Replaced receiver %s: old_key=%s, new_key=%s\n", name, oldStreamKey, streamKey)
		fmt.Printf("  push_url: %s -> %s\n", receiver.PushURL, newReceiver.PushURL)
		fmt.Printf("  flv: %s -> %s\n", receiver.PlayURLs.FLV, newReceiver.PlayURLs.FLV)
		fmt.Printf("  hls: %s -> %s\n", receiver.PlayURLs.HLS, newReceiver.PlayURLs.HLS)
	}

	return config
}