package publisher

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"gopkg.in/yaml.v3"
	"net/http"
	"os"
	"strings"
	"time"
	// "net/url"
)

// SourceConfig 定义流源配置
type SourceConfig struct {
	Type      string            `yaml:"type" json:"type"`             // 源类型: rtsp, http等
	URL       string            `yaml:"url" json:"url"`               // 主源URL
	BackupURL string            `yaml:"backup_url" json:"backup_url"` // 备用源URL
	Headers   map[string]string `yaml:"headers" json:"headers"`       // 请求头
}

// PlayURLs 定义播放地址
type PlayURLs struct {
	FLV string `yaml:"flv" json:"flv,omitempty"` // FLV播放地址
	HLS string `yaml:"hls" json:"hls,omitempty"` // HLS播放地址
}

// ReceiverConfig 定义接收方配置
type ReceiverConfig struct {
	PushURL  string   `yaml:"push_url" json:"push_url"`   // 推流地址
	PlayURLs PlayURLs `yaml:"play_urls" json:"play_urls"` // 播放地址
}

// StreamKeyConfig 定义流密钥配置
type StreamKeyConfig struct {
	Type       string        `yaml:"type" json:"type"`             // 类型: random, fixed
	Value      string        `yaml:"value" json:"value"`           // 固定值或生成的值
	Length     int           `yaml:"length" json:"length"`         // 随机密钥长度
	Expiration string        `yaml:"expiration" json:"expiration"` // 过期时间 (例如: "24h", "720h")
	expiration time.Duration `yaml:"-" json:"-"`                   // 过期时间的内部表示
	Generated  time.Time     `yaml:"generated" json:"generated"`   // 生成时间
}

// StreamConfig 定义单个流配置
type StreamConfig struct {
	Source        SourceConfig              `yaml:"source" json:"source"`                   // 源配置
	LocalPlayURLs PlayURLs                  `yaml:"local_play_urls" json:"local_play_urls"` // 本地播放地址
	Mode          string                    `yaml:"mode" json:"mode"`                       // 模式: primary-backup, all
	Receivers     map[string]ReceiverConfig `yaml:"receivers" json:"receivers"`             // 接收方配置
}

// Config 推流器配置
type Config struct {
	Path       string          `yaml:"path" json:"path"`               // 新增 path
	Protocol   string          `yaml:"protocol" json:"protocol"`       // 推流协议: go, ffmpeg
	BufferSize int             `yaml:"buffer_size" json:"buffer_size"` // 缓冲区大小
	Enabled    bool            `yaml:"enabled" json:"enabled"`         // 是否启用
	StreamKey  StreamKeyConfig `yaml:"streamkey" json:"streamkey"`     // 流密钥配置
	Stream     StreamConfig    `yaml:"stream" json:"stream"`           // 流配置
	ConfigPath string          `yaml:"-" json:"-"`                     // 配置文件路径
}

// DefaultConfig 返回默认配置
func DefaultConfig() Config {
	return Config{
		Path:       "/publisher",  // 添加默认路径
		Protocol:   "go",
		BufferSize: 100,
		Enabled:    false,
		StreamKey: StreamKeyConfig{
			Type:       "random",
			Value:      "",
			Length:     16,
			Expiration: "0", // 0表示永不过期
			Generated:  time.Time{},
		},
		Stream: StreamConfig{
			Source: SourceConfig{
				Type:      "rtsp",
				URL:       "",
				BackupURL: "",
				Headers:   make(map[string]string),
			},
			LocalPlayURLs: PlayURLs{
				FLV: "",
				HLS: "",
			},
			Mode: "primary-backup",
			Receivers: map[string]ReceiverConfig{
				"primary": {
					PushURL: "",
					PlayURLs: PlayURLs{
						FLV: "",
						HLS: "",
					},
				},
				"backup": {
					PushURL: "",
					PlayURLs: PlayURLs{
						FLV: "",
						HLS: "",
					},
				},
			},
		},
	}
}

// GenerateStreamKey 生成或获取流密钥
func (c *Config) GenerateStreamKey() string {
	// 检查是否为固定密钥
	if c.StreamKey.Type == "fixed" {
		// 更新生成时间（即使密钥不变也要更新时间戳）
		c.StreamKey.Generated = time.Now()
		// 保存配置到文件
		c.SaveConfig()
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
				c.SaveConfig()
				return c.StreamKey.Value
			}
		} else {
			// 永不过期，但为了确保配置一致性，更新生成时间
			c.StreamKey.Generated = time.Now()
			// 保存配置到文件
			c.SaveConfig()
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
	c.SaveConfig()
	
	return key
}

// SaveConfig 保存配置到文件
func (c *Config) SaveConfig() error {
	// 如果没有配置文件路径，则不保存
	if c.ConfigPath == "" {
		return nil
	}
	
	// 读取原始配置文件
	data, err := os.ReadFile(c.ConfigPath)
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
		// 更新path字段
		publisher["path"] = c.Path
	}
	
	// 将更新后的配置写回文件
	updatedData, err := yaml.Marshal(rootConfig)
	if err != nil {
		return fmt.Errorf("序列化配置失败: %v", err)
	}
	
	if err := os.WriteFile(c.ConfigPath, updatedData, 0644); err != nil {
		return fmt.Errorf("写入配置文件失败: %v", err)
	}
	
	return nil
}

// GetPath 获取publisher路径
func (c *Config) GetPath() string {
	if c.Path == "" {
		return "/publisher"
	}
	return c.Path
}

// UnmarshalYAML 自定义YAML解析方法
func (c *Config) UnmarshalYAML(value *yaml.Node) error {
	// 先用默认配置填充
	*c = DefaultConfig()
	
	// 解析YAML节点
	type Alias Config
	aux := &struct {
		*Alias
	}{
		Alias: (*Alias)(c),
	}
	
	if err := value.Decode(aux); err != nil {
		return err
	}
	
	return nil
}

// IsStreamKeyExpired 检查流密钥是否过期
func (c *Config) IsStreamKeyExpired() bool {
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
// ReplacePlaceholders 替换配置中的旧流密钥
func (c *Config) ReplacePlaceholders(streamKey string) *Config {
	// 创建配置副本
	config := &Config{
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

// extractKeyFromPushURL 从推流URL中提取流密钥
func extractKeyFromPushURL(pushURL string) string {
	// 推流URL通常是这种格式: rtmp://server.com/live/streamkey
	// 或者: rtmp://server.com/app/streamkey

	// 简单分割方法：按斜杠分割，取最后一部分
	parts := strings.Split(pushURL, "/")
	if len(parts) > 0 {
		lastPart := parts[len(parts)-1]
		// 移除可能存在的查询参数
		if strings.Contains(lastPart, "?") {
			lastPart = strings.Split(lastPart, "?")[0]
		}
		return lastPart
	}

	return ""
}

// GenerateLocalPlayURLs 动态生成本地播放URL
func (c *Config) GenerateLocalPlayURLs(r *http.Request, streamKey string) PlayURLs {
	// 如果没有提供HTTP请求，则返回空的播放URL
	if r == nil {
		return PlayURLs{}
	}

	scheme := getRequestScheme(r)
	baseURL := fmt.Sprintf("%s://%s/live", scheme, r.Host)

	return PlayURLs{
		FLV: fmt.Sprintf("%s/%s.flv", baseURL, streamKey),
		HLS: fmt.Sprintf("%s/%s.m3u8", baseURL, streamKey),
	}
}

// getRequestScheme 获取请求协议
func getRequestScheme(r *http.Request) string {
	// 如果没有提供HTTP请求，则默认使用http
	if r == nil {
		return "http"
	}

	if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
		return proto
	}
	if cfVisitor := r.Header.Get("CF-Visitor"); strings.Contains(cfVisitor, "https") {
		return "https"
	}
	if forwarded := r.Header.Get("Forwarded"); strings.Contains(forwarded, "proto=https") {
		return "https"
	}
	if r.TLS != nil {
		return "https"
	}
	return "http"
}
