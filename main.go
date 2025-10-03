package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"sync"
	"syscall"
	"time"

	"github.com/cloudflare/tableflip"
	"github.com/qist/tvgate/auth"
	"github.com/qist/tvgate/clear"
	"github.com/qist/tvgate/config"
	"github.com/qist/tvgate/config/load"
	"github.com/qist/tvgate/config/watch"
	"github.com/qist/tvgate/dns"
	"github.com/qist/tvgate/groupstats"
	"github.com/qist/tvgate/logger"
	"github.com/qist/tvgate/monitor"
	"github.com/qist/tvgate/publisher"
	"github.com/qist/tvgate/server"
	"github.com/qist/tvgate/web"
)

// 定义任务结构体用于 sync.Pool
type mainTask struct {
	f func()
}

// 创建 sync.Pool 用于复用任务对象
var taskPool = sync.Pool{
	New: func() interface{} {
		return &mainTask{}
	},
}

var shutdownMux sync.Mutex
var shutdownOnce sync.Once

func main() {
	flag.Parse()

	if *config.VersionFlag {
		fmt.Println("程序版本:", config.Version)
		return
	}

	// -------------------------
	// 初始化 tableflip Upgrader（仅非 Windows 平台）
	// -------------------------
	var upg *tableflip.Upgrader
	var err error
	isWindows := runtime.GOOS == "windows"
	if !isWindows {
		upg, err = tableflip.New(tableflip.Options{})
		if err != nil {
			log.Fatalf("无法创建升级器: %v", err)
		}
		defer upg.Stop()
	} else {
		upg = nil
	}

	// -------------------------
	// 配置文件加载
	// -------------------------
	userConfigPath := *config.ConfigFilePath
	configFilePath, err := web.EnsureConfigFile(userConfigPath)
	if err != nil {
		log.Fatalf("确保配置文件失败: %v", err)
	}
	*config.ConfigFilePath = configFilePath
	fmt.Println("使用配置文件:", configFilePath)

	if err := load.LoadConfig(configFilePath); err != nil {
		log.Fatalf("加载配置文件失败: %v", err)
	}
	config.Cfg.SetDefaults()

	// -------------------------
	// 初始化 DNS 解析器
	// -------------------------
	dns.Init()
	
	// -------------------------
	// 初始化 Publisher 推流器
	// -------------------------
	var streamPublishers map[string]*publisher.StreamPublisher
	if len(config.Cfg.Publisher) > 0 {
		// 解析 publisher 配置
		pubConfigs := make(map[string]*publisher.StreamItemConfig)
		publisherPath := "/publisher" // 默认路径
		
		// 提取路径配置
		if pathVal, ok := config.Cfg.Publisher["path"]; ok {
			if pathStr, ok := pathVal.(string); ok {
				publisherPath = pathStr
			}
		}
		
		// 解析流配置
		for name, pubConfig := range config.Cfg.Publisher {
			// 跳过 path 字段
			if name == "path" {
				continue
			}
			
			// 将 interface{} 转换为 publisher.StreamItemConfig
			if configMap, ok := pubConfig.(map[string]interface{}); ok {
				streamConfig := publisher.StreamItemConfig{}
				
				// 解析基本字段
				if protocol, ok := configMap["protocol"].(string); ok {
					streamConfig.Protocol = protocol
				}
				if bufferSize, ok := configMap["buffer_size"].(int); ok {
					streamConfig.BufferSize = bufferSize
				}
				if enabled, ok := configMap["enabled"].(bool); ok {
					streamConfig.Enabled = enabled
				}
				
				// 解析 streamkey 配置
				if streamKeyMap, ok := configMap["streamkey"].(map[string]interface{}); ok {
					streamConfig.StreamKey.Type = getStringValue(streamKeyMap, "type")
					streamConfig.StreamKey.Value = getStringValue(streamKeyMap, "value")
					streamConfig.StreamKey.Length = getIntValue(streamKeyMap, "length")
					streamConfig.StreamKey.Expiration = getStringValue(streamKeyMap, "expiration")
					if generatedStr := getStringValue(streamKeyMap, "generated"); generatedStr != "" {
						if generatedTime, err := time.Parse(time.RFC3339, generatedStr); err == nil {
							streamConfig.StreamKey.Generated = generatedTime
						}
					}
				}
				
				// 解析 stream 配置
				if streamMap, ok := configMap["stream"].(map[string]interface{}); ok {
					// 解析 source 配置
					if sourceMap, ok := streamMap["source"].(map[string]interface{}); ok {
						streamConfig.Stream.Source.Type = getStringValue(sourceMap, "type")
						streamConfig.Stream.Source.URL = getStringValue(sourceMap, "url")
						streamConfig.Stream.Source.BackupURL = getStringValue(sourceMap, "backup_url")
						
						// 解析 headers
						if headersMap, ok := sourceMap["headers"].(map[string]interface{}); ok {
							streamConfig.Stream.Source.Headers = make(map[string]string)
							for k, v := range headersMap {
								if strVal, ok := v.(string); ok {
									streamConfig.Stream.Source.Headers[k] = strVal
								}
							}
						}
					}
					
					// 解析 local_play_urls 配置
					if localPlayURLsMap, ok := streamMap["local_play_urls"].(map[string]interface{}); ok {
						streamConfig.Stream.LocalPlayURLs.FLV = getStringValue(localPlayURLsMap, "flv")
						streamConfig.Stream.LocalPlayURLs.HLS = getStringValue(localPlayURLsMap, "hls")
					}
					
					// 解析 mode
					if mode, ok := streamMap["mode"].(string); ok {
						streamConfig.Stream.Mode = mode
					}
					
					// 解析 receivers 配置
					if receiversMap, ok := streamMap["receivers"].(map[string]interface{}); ok {
						streamConfig.Stream.Receivers = make(map[string]publisher.ReceiverConfig)
						for receiverName, receiverData := range receiversMap {
							if receiverMap, ok := receiverData.(map[string]interface{}); ok {
								receiverConfig := publisher.ReceiverConfig{}
								receiverConfig.PushURL = getStringValue(receiverMap, "push_url")
								
								// 解析 play_urls
								if playURLsMap, ok := receiverMap["play_urls"].(map[string]interface{}); ok {
									receiverConfig.PlayURLs.FLV = getStringValue(playURLsMap, "flv")
									receiverConfig.PlayURLs.HLS = getStringValue(playURLsMap, "hls")
								}
								
								streamConfig.Stream.Receivers[receiverName] = receiverConfig
							}
						}
					}
				}
				
				pubConfigs[name] = &streamConfig
			}
		}
		
		// 创建 PublisherConfig
		publisherConfig := &publisher.PublisherConfig{
			Path:    publisherPath,
			Streams: pubConfigs,
		}
		
		// 初始化推流器
		streamPublisher, err := publisher.Init(publisherConfig)
		if err != nil {
			log.Printf("初始化推流器失败: %v", err)
		} else if streamPublisher != nil {
			streamPublishers = make(map[string]*publisher.StreamPublisher)
			// 添加流到publisher
			for name, pubConfig := range pubConfigs {
				if pubConfig.Enabled {
					pubConfig.ConfigPath = *config.ConfigFilePath
					_, err := streamPublisher.AddStream(name, pubConfig, nil)
					if err != nil {
						log.Printf("添加流 %s 失败: %v", name, err)
					} else {
						log.Printf("流 %s 添加成功", name)
						streamPublishers[name] = streamPublisher
					}
				}
			}
			
			if len(streamPublishers) > 0 {
				// 注册publisher HTTP处理器
				http.Handle(publisherConfig.Path+"/", publisher.NewHTTPHandler(streamPublisher))
				log.Printf("推流器 HTTP 处理器已注册，路径: %s", publisherConfig.Path)
			}
		}
	}
	
	// 添加调试信息，确认DNS初始化是否正常工作
	// resolver := dns.GetInstance()
	// resolvers := resolver.GetResolvers()
	// fmt.Printf("DNS Resolvers: %v\n", resolvers)
	
	// -------------------------
	// 初始化代理组统计
	// -------------------------
	groupstats.InitProxyGroups()
	for _, group := range config.Cfg.ProxyGroups {
		group.Stats = &config.GroupStats{
			ProxyStats: make(map[string]*config.ProxyStats),
		}
	}

	// -------------------------
	// 初始化全局 token 管理器
	// -------------------------
	if config.Cfg.GlobalAuth.TokensEnabled {
		auth.GlobalTokenManager = auth.NewGlobalTokenManagerFromConfig(&config.Cfg.GlobalAuth)
	} else {
		auth.GlobalTokenManager = nil
	}

	tm := &auth.TokenManager{
		Enabled:       true,
		StaticTokens:  make(map[string]*auth.SessionInfo),
		DynamicTokens: make(map[string]*auth.SessionInfo),
	}

	// token 清理任务
	cleanupTask := taskPool.Get().(*mainTask)
	cleanupTask.f = func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			tm.CleanupExpiredSessions()
		}
	}
	go func() {
		defer func() {
			cleanupTask.f = nil
			taskPool.Put(cleanupTask)
		}()
		cleanupTask.f()
	}()

	// -------------------------
	// 启动监控 & 清理任务
	// -------------------------
	stopActiveClients := make(chan struct{})
	stopStartSystemStatsUpdater := make(chan struct{})
	stopCleaner := make(chan struct{})
	stopAccessCleaner := make(chan struct{})
	stopProxyStats := make(chan struct{})

	startTask := func(f func()) {
		task := taskPool.Get().(*mainTask)
		task.f = f
		go func() {
			defer func() {
				task.f = nil
				taskPool.Put(task)
			}()
			task.f()
		}()
	}

	startTask(func() { monitor.ActiveClients.StartCleaner(30*time.Second, 20*time.Second, stopActiveClients) })
	startTask(func() { monitor.StartSystemStatsUpdater(30*time.Second, stopStartSystemStatsUpdater) })
	startTask(func() { clear.StartRedirectChainCleaner(10*time.Minute, 30*time.Minute, stopCleaner) })
	startTask(func() { clear.StartAccessCacheCleaner(10*time.Minute, 30*time.Minute, stopAccessCleaner) })
	startTask(func() { clear.StartGlobalProxyStatsCleaner(10*time.Minute, 2*time.Hour, stopProxyStats) })

	// -------------------------
	// 日志
	// -------------------------
	logger.SetupLogger(logger.LogConfig{
		Enabled:    config.Cfg.Log.Enabled,
		File:       config.Cfg.Log.File,
		MaxSizeMB:  config.Cfg.Log.MaxSizeMB,
		MaxBackups: config.Cfg.Log.MaxBackups,
		MaxAgeDays: config.Cfg.Log.MaxAgeDays,
		Compress:   config.Cfg.Log.Compress,
	})

	// -------------------------
	// 启动配置文件监控
	// -------------------------
	watchTask := taskPool.Get().(*mainTask)
	watchTask.f = func() {
		watch.WatchConfigFile(configFilePath, upg)
	}
	go func() {
		defer func() {
			watchTask.f = nil
			taskPool.Put(watchTask)
		}()
		watchTask.f()
	}()

	// -------------------------
	// context 管理
	// -------------------------
	config.ServerCtx, config.Cancel = context.WithCancel(context.Background())

	// -------------------------
	// 启动 HTTP Server（支持 tableflip 热更）
	// -------------------------
	var wg sync.WaitGroup
	startServer := func(port int) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			addr := fmt.Sprintf(":%d", port)
			if err := server.StartHTTPServer(config.ServerCtx, addr, upg); err != nil && err != context.Canceled {
				logger.LogPrintf("❌ 启动 HTTP 服务失败 %s: %v", addr, err)
			}
		}()
	}

	if config.Cfg.Server.Port > 0 {
		startServer(config.Cfg.Server.Port)
	}
	if config.Cfg.Server.HTTPPort > 0 {
		startServer(config.Cfg.Server.HTTPPort)
	}
	if config.Cfg.Server.TLS.HTTPSPort > 0 {
		startServer(config.Cfg.Server.TLS.HTTPSPort)
	}

	wg.Wait() // 阻塞等待所有 server

	// -------------------------
	// 捕获系统退出信号
	// -------------------------
	signalTask := taskPool.Get().(*mainTask)
	signalTask.f = func() {
		sigChan := make(chan os.Signal, 1)
		signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
		<-sigChan
		fmt.Println("收到退出信号，开始优雅退出")
		gracefulShutdown(stopCleaner, stopAccessCleaner, stopProxyStats, stopActiveClients, stopStartSystemStatsUpdater)
		if !isWindows && upg != nil {
			upg.Exit()
		} else {
			os.Exit(0)
		}
	}
	go func() {
		defer func() {
			signalTask.f = nil
			taskPool.Put(signalTask)
		}()
		signalTask.f()
	}()

	// -------------------------
	// tableflip 准备完成（仅非 Windows）
	// -------------------------
	if !isWindows && upg != nil {
		if err := upg.Ready(); err != nil {
			log.Fatalf("升级器准备失败: %v", err)
		}
	}

	<-config.ServerCtx.Done()
	gracefulShutdown(stopCleaner, stopAccessCleaner, stopProxyStats, stopActiveClients, stopStartSystemStatsUpdater)
}

// getStringValue 从 map 中获取字符串值
func getStringValue(m map[string]interface{}, key string) string {
	if val, ok := m[key].(string); ok {
		return val
	}
	return ""
}

// getIntValue 从 map 中获取整数值
func getIntValue(m map[string]interface{}, key string) int {
	if val, ok := m[key].(int); ok {
		return val
	}
	return 0
}

func gracefulShutdown(stopCleaner, stopAccessCleaner, stopProxyStats, stopActiveClients, stopStartSystemStatsUpdater chan struct{}) {
	shutdownOnce.Do(func() {
		shutdownMux.Lock()
		defer shutdownMux.Unlock()

		if config.Cancel != nil {
			config.Cancel()
		}

		close(stopCleaner)
		close(stopAccessCleaner)
		close(stopProxyStats)
		close(stopActiveClients)
		close(stopStartSystemStatsUpdater)

		time.Sleep(100 * time.Millisecond)
		fmt.Println("优雅退出完成")
	})
}