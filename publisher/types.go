package publisher

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"sync"
	"time"
	
	"github.com/qist/tvgate/logger"
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

// Stream 流媒体结构
type Stream struct {
	// 流ID
	id string
	
	// 流配置
	config *StreamItemConfig
	
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
	
	// HLS推流器
	hlsPusher *HLSPusher
	
	// 流源
	source StreamSource
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

// HLSPusher HLS推流器
type HLSPusher struct {
	stream    *Stream
	path      string
	running   bool
	done      chan struct{}
	pushURL   string
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

// Start 启动流
func (s *Stream) Start() {
	if s.running {
		return
	}
	
	s.running = true
	s.done = make(chan struct{})
	
	// 根据源类型创建对应的流源
	sourceURL := s.config.Stream.Source.URL
	switch s.config.Stream.Source.Type {
	case "rtsp":
		// 对于RTSP源，我们可能需要支持备份URL
		s.source = NewRTSPSource(sourceURL, "") // 可以扩展支持备份URL
	case "http", "https":
		s.source = NewHTTPSource(sourceURL)
	case "file":
		s.source = NewFileSource(sourceURL)
	default:
		logger.LogPrintf("Unsupported stream source type: %s", s.config.Stream.Source.Type)
		return
	}
	
	// 启动拉流协程
	go s.pullStream()
	
	// 启动数据处理协程
	go s.processData()
	
	// 启动HLS推流器（如果配置了推流地址）
	if s.hlsPusher == nil && s.config.Stream.Receivers != nil {
		s.hlsPusher = NewHLSPusher(s, "")
	}
	
	if s.hlsPusher != nil {
		for _, receiver := range s.config.Stream.Receivers {
			if receiver.PushURL != "" {
				// 启动推流
				if err := s.hlsPusher.Start(receiver.PushURL); err != nil {
					logger.LogPrintf("启动HLS推流器失败: %v", err)
				}
				break // 只启动第一个配置的推流器
			}
		}
	}
}

// Stop 停止流
func (s *Stream) Stop() {
	if !s.running {
		return
	}
	
	s.running = false
	close(s.done)
	
	// 停止流源
	if s.source != nil {
		s.source.Close()
	}
	
	// 停止HLS推流器
	if s.hlsPusher != nil {
		s.hlsPusher.Stop()
	}
	
	// 清理客户端连接
	s.clientsMu.Lock()
	defer s.clientsMu.Unlock()
	
	for _, client := range s.clients {
		s.removeClient(client)
	}
	
	// 清空客户端列表
	s.clients = make(map[string]*Client)
}

// pullStream 从源地址拉取流
func (s *Stream) pullStream() {
	if s.source == nil {
		logger.LogPrintf("Stream %s has no source configured", s.id)
		return
	}
	
	// 从流源读取数据包并推送到缓冲区
	for {
		select {
		case <-s.done:
			return
		case packet, ok := <-s.source.Packets():
			if !ok {
				// 流源已关闭
				logger.LogPrintf("Stream source for %s closed", s.id)
				return
			}
			// 将数据推送到缓冲区
			s.PushData(packet.Data)
		}
	}
}

// AddClient 添加客户端
func (s *Stream) AddClient(client *Client) {
	s.clientsMu.Lock()
	defer s.clientsMu.Unlock()
	
	s.clients[client.id] = client
}

// removeClient 私有方法，移除客户端
func (s *Stream) removeClient(client *Client) {
	// 实际断开客户端连接的逻辑
	fmt.Printf("Client %s disconnected from stream %s\n", client.id, s.id)
}

// CheckStreamKeyExpiration 检查流密钥过期
func (s *Stream) CheckStreamKeyExpiration() {
	// 检查流密钥是否过期
	if s.config.IsStreamKeyExpired() {
		logger.LogPrintf("Stream key for %s expired, stopping stream", s.id)
		s.Stop()
	}
}

// processData 处理流数据
func (s *Stream) processData() {
	for {
		select {
		case <-s.done:
			return
		case data := <-s.buffer:
			// 处理数据
			s.clientsMu.RLock()
			clients := make([]*Client, 0, len(s.clients))
			for _, client := range s.clients {
				clients = append(clients, client)
			}
			s.clientsMu.RUnlock()
			
			// 将数据发送给所有客户端
			for _, client := range clients {
				// 这里应该实际发送数据给客户端
				fmt.Printf("Pushing %d bytes to client %s at %s\n", len(data), client.id, client.pushURL)
			}
		}
	}
}

// HandleLocalPlay 处理本地播放请求
func (s *Stream) HandleLocalPlay(w http.ResponseWriter, r *http.Request) {
	// 实现本地播放逻辑
	w.Header().Set("Content-Type", "video/mp4")
	fmt.Fprintf(w, "Local play for stream %s", s.id)
}

// HandlePublish 处理推流请求
func (s *Stream) HandlePublish(w http.ResponseWriter, r *http.Request) {
	// 实现推流处理逻辑
	fmt.Fprintf(w, "Publishing stream %s", s.id)
}

// GenerateStreamKey 生成或获取流密钥
func (c *StreamItemConfig) GenerateStreamKey() string {
	// 检查是否为固定密钥
	if c.StreamKey.Type == "fixed" {
		// 更新生成时间（即使密钥不变也要更新时间戳）
		c.StreamKey.Generated = time.Now()
		// 保存配置到文件
		// c.SaveConfig() // 暂时注释掉避免循环依赖
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
				// c.SaveConfig() // 暂时注释掉避免循环依赖
				return c.StreamKey.Value
			}
		} else {
			// 永不过期，但为了确保配置一致性，更新生成时间
			c.StreamKey.Generated = time.Now()
			// 保存配置到文件
			// c.SaveConfig() // 暂时注释掉避免循环依赖
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
	
	// 保存配置到文件
	// c.SaveConfig() // 暂时注释掉避免循环依赖
	
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