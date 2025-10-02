package publisher

import (
	"crypto/rand"
	"encoding/hex"
	"strings"
	"time"
	"fmt"
	"net/http"
	"gopkg.in/yaml.v3"
	"os"
)

// SourceConfig 定义流源配置
type SourceConfig struct {
	Type      string            `yaml:"type" json:"type"`           // 源类型: rtsp, http等
	URL       string            `yaml:"url" json:"url"`             // 主源URL
	BackupURL string            `yaml:"backup_url" json:"backup_url"` // 备用源URL
	Headers   map[string]string `yaml:"headers" json:"headers"`     // 请求头
}

// PlayURLs 定义播放地址
type PlayURLs struct {
	FLV string `yaml:"flv" json:"flv,omitempty"` // FLV播放地址
	HLS string `yaml:"hls" json:"hls,omitempty"` // HLS播放地址
}

// ReceiverConfig 定义接收方配置
type ReceiverConfig struct {
	PushURL   string   `yaml:"push_url" json:"push_url"`     // 推流地址
	PlayURLs  PlayURLs `yaml:"play_urls" json:"play_urls"`   // 播放地址
}

// StreamKeyConfig 定义流密钥配置
type StreamKeyConfig struct {
	Type       string        `yaml:"type" json:"type"`           // 类型: random, fixed
	Value      string        `yaml:"value" json:"value"`         // 固定值或生成的值
	Length     int           `yaml:"length" json:"length"`       // 随机密钥长度
	Expiration string        `yaml:"expiration" json:"expiration"` // 过期时间 (例如: "24h", "720h")
	expiration time.Duration `yaml:"-" json:"-"`                 // 过期时间的内部表示
	Generated  time.Time     `yaml:"generated" json:"generated"` // 生成时间
}

// StreamConfig 定义单个流配置
type StreamConfig struct {
	Source        SourceConfig             `yaml:"source" json:"source"`               // 源配置
	LocalPlayURLs PlayURLs                 `yaml:"local_play_urls" json:"local_play_urls"` // 本地播放地址
	Mode          string                   `yaml:"mode" json:"mode"`                   // 模式: primary-backup, all
	Receivers     map[string]ReceiverConfig `yaml:"receivers" json:"receivers"`         // 接收方配置
}

// Config 推流器配置
type Config struct {
	Protocol   string         `yaml:"protocol" json:"protocol"`     // 推流协议: go, ffmpeg
	BufferSize int            `yaml:"buffer_size" json:"buffer_size"` // 缓冲区大小
	Enabled    bool           `yaml:"enabled" json:"enabled"`       // 是否启用
	StreamKey  StreamKeyConfig `yaml:"streamkey" json:"streamkey"`   // 流密钥配置
	Stream     StreamConfig   `yaml:"stream" json:"stream"`         // 流配置
	ConfigPath string         `yaml:"-" json:"-"`                  // 配置文件路径
}

// DefaultConfig 返回默认配置
func DefaultConfig() Config {
	return Config{
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

// GenerateStreamKey 生成流密钥
func (c *Config) GenerateStreamKey() string {
	// 如果配置了固定密钥（value不为空且未过期），直接返回
	if c.StreamKey.Value != "" {
		// 解析过期时间
		if c.StreamKey.Expiration != "0" && c.StreamKey.expiration == 0 {
			exp, err := time.ParseDuration(c.StreamKey.Expiration)
			if err == nil {
				c.StreamKey.expiration = exp
			}
		}
		
		// 检查是否过期
		if c.StreamKey.expiration > 0 && !c.StreamKey.Generated.IsZero() {
			if time.Since(c.StreamKey.Generated) < c.StreamKey.expiration {
				return c.StreamKey.Value
			}
		} else {
			// 永不过期
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
		// 如果随机生成失败，使用时间戳
		return time.Now().Format("20060102150405")
	}
	
	streamKey := hex.EncodeToString(bytes)
	
	// 更新配置中的值和生成时间
	c.StreamKey.Value = streamKey
	c.StreamKey.Generated = time.Now()
	
	// 保存配置到文件
	c.SaveConfig()
	
	return streamKey
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
	var config map[string]interface{}
	if err := yaml.Unmarshal(data, &config); err != nil {
		return fmt.Errorf("解析配置文件失败: %v", err)
	}
	
	// 更新publisher配置
	// 注意：这里我们无法直接更新配置，因为缺少streamID
	// 实际的保存应该在StreamPublisher中实现
	
	return nil
}

// ReplacePlaceholders 替换配置中的占位符
func (c *Config) ReplacePlaceholders(streamKey string) *Config {
	// 创建配置副本
	config := &Config{
		Protocol:   c.Protocol,
		BufferSize: c.BufferSize,
		Enabled:    c.Enabled,
		StreamKey:  c.StreamKey,
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
	
	// 替换源URL中的占位符
	config.Stream.Source.URL = strings.ReplaceAll(c.Stream.Source.URL, "$(streamkey.value)", streamKey)
	config.Stream.Source.BackupURL = strings.ReplaceAll(c.Stream.Source.BackupURL, "$(streamkey.value)", streamKey)
	
	// 替换Headers中的占位符
	for key, value := range config.Stream.Source.Headers {
		config.Stream.Source.Headers[key] = strings.ReplaceAll(value, "$(streamkey.value)", streamKey)
	}
	
	// 处理本地播放URL
	// 如果配置了本地播放URL，则替换占位符；否则保持为空，由系统动态生成
	if config.Stream.LocalPlayURLs.FLV != "" {
		config.Stream.LocalPlayURLs.FLV = strings.ReplaceAll(c.Stream.LocalPlayURLs.FLV, "$(streamkey.value)", streamKey)
		
		// 如果FLV播放地址以"/"结尾，自动拼接streamKey.flv
		if strings.HasSuffix(config.Stream.LocalPlayURLs.FLV, "/") {
			config.Stream.LocalPlayURLs.FLV = config.Stream.LocalPlayURLs.FLV + streamKey + ".flv"
		}
	}
	
	if config.Stream.LocalPlayURLs.HLS != "" {
		config.Stream.LocalPlayURLs.HLS = strings.ReplaceAll(c.Stream.LocalPlayURLs.HLS, "$(streamkey.value)", streamKey)
		
		// 如果HLS播放地址以"/"结尾，自动拼接streamKey.m3u8
		if strings.HasSuffix(config.Stream.LocalPlayURLs.HLS, "/") {
			config.Stream.LocalPlayURLs.HLS = config.Stream.LocalPlayURLs.HLS + streamKey + ".m3u8"
		}
	}
	
	// 替换接收方配置中的占位符
	for name, receiver := range c.Stream.Receivers {
		// 处理推流地址
		pushURL := strings.ReplaceAll(receiver.PushURL, "$(streamkey.value)", streamKey)
		
		// 如果推流地址以"/"结尾，自动拼接streamKey
		if strings.HasSuffix(pushURL, "/") {
			pushURL = pushURL + streamKey
		}
		
		// 处理播放地址
		playURLs := PlayURLs{}
		
		// 处理FLV播放地址
		if receiver.PlayURLs.FLV != "" {
			flvURL := strings.ReplaceAll(receiver.PlayURLs.FLV, "$(streamkey.value)", streamKey)
			
			// 如果FLV播放地址以"/"结尾，自动拼接streamKey.flv
			if strings.HasSuffix(flvURL, "/") {
				flvURL = flvURL + streamKey + ".flv"
			}
			playURLs.FLV = flvURL
		}
		
		// 处理HLS播放地址
		if receiver.PlayURLs.HLS != "" {
			hlsURL := strings.ReplaceAll(receiver.PlayURLs.HLS, "$(streamkey.value)", streamKey)
			
			// 如果HLS播放地址以"/"结尾，自动拼接streamKey.m3u8
			if strings.HasSuffix(hlsURL, "/") {
				hlsURL = hlsURL + streamKey + ".m3u8"
			}
			playURLs.HLS = hlsURL
		}
		
		newReceiver := ReceiverConfig{
			PushURL:  pushURL,
			PlayURLs: playURLs,
		}
		config.Stream.Receivers[name] = newReceiver
	}
	
	return config
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