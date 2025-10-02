package publisher

import (
	"fmt"
	"sync"
	"net/http"
	"io"
	"time"
	"github.com/qist/tvgate/logger"
	"strings"
	"gopkg.in/yaml.v3"
	"os"
	"net/url"
)

// StreamPublisher 流推流器结构
type StreamPublisher struct {
	// 推流配置
	config *Config
	
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
	config *Config
	
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
func NewStreamPublisher(config *Config, configPath string) *StreamPublisher {
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
	
	fmt.Printf("Stream publisher started with protocol %s\n", sp.config.Protocol)
	return nil
}

// startExpirationChecker 启动过期检查器
func (sp *StreamPublisher) startExpirationChecker() {
	sp.expirationChecker = time.NewTicker(1 * time.Minute) // 每分钟检查一次
	
	go func() {
		for {
			select {
			case <-sp.expirationChecker.C:
				sp.checkStreamKeyExpirations()
			case <-sp.done:
				return
			}
		}
	}()
}

// checkStreamKeyExpirations 检查所有流的streamkey是否过期
func (sp *StreamPublisher) checkStreamKeyExpirations() {
	sp.streamsMu.Lock()
	defer sp.streamsMu.Unlock()
	
	for streamID, stream := range sp.streams {
		if stream.config.IsStreamKeyExpired() {
			fmt.Printf("Stream %s key expired, regenerating...\n", streamID)
			
			// 保存旧的配置用于后续更新
			oldConfig := stream.config
			
			// 重新生成流密钥（这会自动保存配置）
			newStreamKey := oldConfig.GenerateStreamKey()
			
			// 替换配置中的占位符，使用最新的streamKey
			resolvedConfig := oldConfig.ReplacePlaceholders(newStreamKey)
			
			// 生成本地播放URL（使用空请求，后续会根据实际请求动态更新）
			localPlayURLs := oldConfig.GenerateLocalPlayURLs(nil, newStreamKey)
			
			// 更新resolvedConfig中的本地播放URL
			resolvedConfig.Stream.LocalPlayURLs = localPlayURLs
			
			// 保存处理后的配置到文件
			sp.SaveStreamKeyToConfig(streamID, resolvedConfig)
			
			// 停止当前流
			stream.Stop()
			
			// 重新创建流
			newStream := &Stream{
				id:            streamID,
				config:        resolvedConfig,
				streamKey:     newStreamKey,
				clients:       make(map[string]*Client),
				buffer:        make(chan []byte, resolvedConfig.BufferSize),
				running:       true,
				done:          make(chan struct{}),
				publisher:     sp,
				localPlayURLs: localPlayURLs,
			}
			
			// 根据模式添加客户端
			switch resolvedConfig.Stream.Mode {
			case "primary-backup":
				// 添加主推流客户端
				if primary, exists := resolvedConfig.Stream.Receivers["primary"]; exists && primary.PushURL != "" {
					client := &Client{
						id:        fmt.Sprintf("%s-primary", streamID),
						pushURL:   primary.PushURL,
						playURLs:  primary.PlayURLs,
						isPrimary: true,
					}
					newStream.AddClient(client)
				}
				
				// 添加备用推流客户端
				if backup, exists := resolvedConfig.Stream.Receivers["backup"]; exists && backup.PushURL != "" {
					client := &Client{
						id:        fmt.Sprintf("%s-backup", streamID),
						pushURL:   backup.PushURL,
						playURLs:  backup.PlayURLs,
						isPrimary: false,
					}
					newStream.AddClient(client)
				}
				
			case "all":
				// 添加所有接收方
				for name, receiver := range resolvedConfig.Stream.Receivers {
					if receiver.PushURL != "" {
						client := &Client{
							id:        fmt.Sprintf("%s-%s", streamID, name),
							pushURL:   receiver.PushURL,
							playURLs:  receiver.PlayURLs,
							isPrimary: name == "primary",
						}
						newStream.AddClient(client)
					}
				}
			}
			
			// 替换旧流
			sp.streams[streamID] = newStream
			
			// 启动新流
			newStream.Start()
			
			fmt.Printf("Stream %s restarted with new key: %s\n", streamID, newStreamKey)
		}
	}
}

// SaveStreamKeyToConfig 将生成的streamkey保存到配置文件中
func (sp *StreamPublisher) SaveStreamKeyToConfig(streamID string, config *Config) error {
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
			// 更新streamkey的值和生成时间
			if streamKey, ok := streamConfig["streamkey"].(map[string]interface{}); ok {
				streamKey["value"] = config.StreamKey.Value
				streamKey["generated"] = config.StreamKey.Generated.Format(time.RFC3339)
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
						if receiver, ok := receivers[receiverName].(map[string]interface{}); ok {
							// 更新push_url
							if receiverData.PushURL != "" {
								receiver["push_url"] = receiverData.PushURL
								fmt.Printf("Updated push_url for %s: %s\n", receiverName, receiverData.PushURL)
							}
							
							// 更新play_urls
							if playURLs, ok := receiver["play_urls"].(map[string]interface{}); ok {
								if receiverData.PlayURLs.FLV != "" {
									playURLs["flv"] = receiverData.PlayURLs.FLV
									fmt.Printf("Updated FLV play_url for %s: %s\n", receiverName, receiverData.PlayURLs.FLV)
								}
								if receiverData.PlayURLs.HLS != "" {
									playURLs["hls"] = receiverData.PlayURLs.HLS
									fmt.Printf("Updated HLS play_url for %s: %s\n", receiverName, receiverData.PlayURLs.HLS)
								}
							}
						} else {
							fmt.Printf("Receiver %s not found in config file\n", receiverName)
						}
					}
				} else {
					fmt.Printf("Receivers not found in stream config\n")
				}
			} else {
				fmt.Printf("Stream config not found\n")
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

// Stop 停止推流服务
func (sp *StreamPublisher) Stop() error {
	sp.running = false
	
	// 停止过期检查器
	if sp.expirationChecker != nil {
		sp.expirationChecker.Stop()
	}
	close(sp.done)
	
	// 停止所有流
	sp.streamsMu.Lock()
	for _, stream := range sp.streams {
		stream.Stop()
	}
	sp.streamsMu.Unlock()
	
	return nil
}

// AddStream 添加流
func (sp *StreamPublisher) AddStream(streamID string, config *Config, r *http.Request) *Stream {
	sp.streamsMu.Lock()
	defer sp.streamsMu.Unlock()
	
	// 检查配置是否为nil
	if config == nil {
		logger.LogPrintf("Failed to add stream %s: config is nil", streamID)
		return nil
	}
	
	// 生成或使用流密钥
	streamKey := config.GenerateStreamKey()
	
	// 替换配置中的占位符，使用最新的streamKey
	resolvedConfig := config.ReplacePlaceholders(streamKey)
	
	// 生成本地播放URL
	var req *http.Request
	if r != nil {
		req = r
		// 更新resolvedConfig中的本地播放URL
		resolvedConfig.Stream.LocalPlayURLs = config.GenerateLocalPlayURLs(req, streamKey)
	} else {
		// 创建一个默认的请求用于生成本地播放URL
		req = &http.Request{
			Host: "localhost:8080",
			URL:  &url.URL{Path: "/"},
		}
		// 更新resolvedConfig中的本地播放URL
		resolvedConfig.Stream.LocalPlayURLs = config.GenerateLocalPlayURLs(req, streamKey)
	}
	
	// 总是保存配置到文件，确保URL是最新的
	sp.SaveStreamKeyToConfig(streamID, resolvedConfig)
	
	stream := &Stream{
		id:             streamID,
		config:         resolvedConfig,
		streamKey:      streamKey,
		clients:        make(map[string]*Client),
		buffer:         make(chan []byte, resolvedConfig.BufferSize),
		running:        true,
		done:           make(chan struct{}),
		publisher:      sp,
		localPlayURLs:  resolvedConfig.Stream.LocalPlayURLs,
	}
	
	sp.streams[streamID] = stream
	
	// 根据模式添加客户端
	switch resolvedConfig.Stream.Mode {
	case "primary-backup":
		// 添加主推流客户端
		if primary, exists := resolvedConfig.Stream.Receivers["primary"]; exists && primary.PushURL != "" {
			client := &Client{
				id:        fmt.Sprintf("%s-primary", streamID),
				pushURL:   primary.PushURL,
				playURLs:  primary.PlayURLs,
				isPrimary: true,
			}
			stream.AddClient(client)
		}
		
		// 添加备用推流客户端
		if backup, exists := resolvedConfig.Stream.Receivers["backup"]; exists && backup.PushURL != "" {
			client := &Client{
				id:        fmt.Sprintf("%s-backup", streamID),
				pushURL:   backup.PushURL,
				playURLs:  backup.PlayURLs,
				isPrimary: false,
			}
			stream.AddClient(client)
		}
		
	case "all":
		// 添加所有接收方
		for name, receiver := range resolvedConfig.Stream.Receivers {
			if receiver.PushURL != "" {
				client := &Client{
					id:        fmt.Sprintf("%s-%s", streamID, name),
					pushURL:   receiver.PushURL,
					playURLs:  receiver.PlayURLs,
					isPrimary: name == "primary",
				}
				stream.AddClient(client)
			}
		}
	}
	
	// 启动流处理
	stream.Start()
	
	fmt.Printf("Stream %s added with source URL: %s, mode: %s\n", streamID, resolvedConfig.Stream.Source.URL, resolvedConfig.Stream.Mode)
	
	return stream
}

// RemoveStream 移除流
func (sp *StreamPublisher) RemoveStream(streamID string) {
	sp.streamsMu.Lock()
	defer sp.streamsMu.Unlock()
	
	if stream, exists := sp.streams[streamID]; exists {
		stream.Stop()
		delete(sp.streams, streamID)
		fmt.Printf("Stream %s removed\n", streamID)
	}
}

// GetStream 获取流
func (sp *StreamPublisher) GetStream(streamID string) *Stream {
	sp.streamsMu.RLock()
	defer sp.streamsMu.RUnlock()
	
	return sp.streams[streamID]
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
		// 使用 gortsplib 拉流
		s.pullRTSPStream()
	case "http", "hls":
		// 使用 http client 或 ffmpeg 拉流
		s.pullHTTPStream()
	case "file":
		// 直接用 ffmpeg / go 内部解码器读取文件
		s.pullFileStream()
	case "device":
		// 打开本地采集设备
		s.pullDeviceStream()
	case "screen":
		// 桌面采集
		s.pullScreenStream()
	case "custom":
		// 插件扩展
		s.pullCustomStream()
	default:
		logger.LogPrintf("Unknown source type: %s for stream %s", s.config.Stream.Source.Type, s.id)
	}
}

// pullRTSPStream 拉取RTSP流数据（占位实现）
func (s *Stream) pullRTSPStream() {
	// 这里应该实现RTSP流的拉取逻辑
	// 由于是纯转发，我们可以简化处理
	// 在实际应用中，这里需要实现具体的RTSP拉流协议
	logger.LogPrintf("Pulling RTSP stream %s from %s (not implemented)", s.id, s.config.Stream.Source.URL)
}

// pullHTTPStream 拉取HTTP/HLS流数据
func (s *Stream) pullHTTPStream() {
	// 定时拉取流数据
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()
	
	for {
		select {
		case <-s.done:
			return
		case <-ticker.C:
			// 拉取HTTP流数据
			s.pullHTTPData()
		}
	}
}

// pullHTTPData 拉取HTTP流数据
func (s *Stream) pullHTTPData() {
	req, err := http.NewRequest("GET", s.config.Stream.Source.URL, nil)
	if err != nil {
		logger.LogPrintf("Failed to create request for stream %s from %s: %v", s.id, s.config.Stream.Source.URL, err)
		return
	}

	// 添加自定义headers
	for key, value := range s.config.Stream.Source.Headers {
		req.Header.Add(key, value)
	}

	resp, err := s.publisher.httpClient.Do(req)
	if err != nil {
		logger.LogPrintf("Failed to pull stream %s from %s: %v", s.id, s.config.Stream.Source.URL, err)
		return
	}
	defer resp.Body.Close()
	
	if resp.StatusCode != http.StatusOK {
		logger.LogPrintf("Failed to pull stream %s, status code: %d", s.id, resp.StatusCode)
		return
	}
	
	// 读取数据
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		logger.LogPrintf("Failed to read stream %s data: %v", s.id, err)
		return
	}
	
	// 推送数据
	s.PushData(data)
	
	logger.LogPrintf("Pulled %d bytes for stream %s from %s", len(data), s.id, s.config.Stream.Source.URL)
}

// pullFileStream 从文件拉取流数据（占位实现）
func (s *Stream) pullFileStream() {
	logger.LogPrintf("Pulling file stream %s from %s (not implemented)", s.id, s.config.Stream.Source.URL)
}

// pullDeviceStream 从设备拉取流数据（占位实现）
func (s *Stream) pullDeviceStream() {
	logger.LogPrintf("Pulling device stream %s (not implemented)", s.id)
}

// pullScreenStream 从屏幕采集拉取流数据（占位实现）
func (s *Stream) pullScreenStream() {
	logger.LogPrintf("Pulling screen stream %s (not implemented)", s.id)
}

// pullCustomStream 从自定义源拉取流数据（占位实现）
func (s *Stream) pullCustomStream() {
	logger.LogPrintf("Pulling custom stream %s (not implemented)", s.id)
}

// processData 处理数据
func (s *Stream) processData() {
	for {
		select {
		case data, ok := <-s.buffer:
			if !ok {
				return
			}
			
			// 将数据推送到所有客户端
			s.clientsMu.RLock()
			clients := make([]*Client, 0, len(s.clients))
			for _, client := range s.clients {
				clients = append(clients, client)
			}
			s.clientsMu.RUnlock()
			
			for _, client := range clients {
				// 实际推送数据到客户端的逻辑
				fmt.Printf("Pushing %d bytes to client %s at %s\n", len(data), client.id, client.pushURL)
				
				// 这里应该实现实际的推流逻辑，例如RTMP推流
				// 由于是纯转发，我们可以简化处理
				// 在实际应用中，这里需要实现具体的推流协议
			}
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

// GetLocalPlayURLs 获取本地播放URL
func (s *Stream) GetLocalPlayURLs() PlayURLs {
	return s.localPlayURLs
}

// HandleLocalPlay 处理本地播放请求
func (s *Stream) HandleLocalPlay(w http.ResponseWriter, r *http.Request) {
	// 根据请求路径确定播放类型
	path := r.URL.Path
	var sourceURL string
	
	switch {
	case strings.HasSuffix(path, ".m3u8"):
		sourceURL = s.config.Stream.Source.URL
	case strings.HasSuffix(path, ".flv"):
		sourceURL = s.config.Stream.Source.URL
	default:
		http.Error(w, "Unsupported format", http.StatusBadRequest)
		return
	}
	
	if sourceURL == "" {
		http.Error(w, "Stream source not configured", http.StatusNotFound)
		return
	}
	
	// 转发源流数据给客户端
	resp, err := s.publisher.httpClient.Get(sourceURL)
	if err != nil {
		http.Error(w, "Failed to fetch stream", http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()
	
	// 复制响应头
	for key, values := range resp.Header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	
	w.WriteHeader(resp.StatusCode)
	
	// 转发数据
	_, err = io.Copy(w, resp.Body)
	if err != nil {
		logger.LogPrintf("Failed to forward stream data: %v", err)
	}
}