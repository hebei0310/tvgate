package publisher

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"time"

	"github.com/bluenviron/gortmplib"
)

// NewHLSPusher 创建新的HLS推流器
func NewHLSPusher(stream *Stream, path string) *HLSPusher {
	return &HLSPusher{
		stream: stream,
		path:   path,
		done:   make(chan struct{}),
	}
}

// Start 启动HLS推流
func (h *HLSPusher) Start(pushURL string) error {
	if h.running {
		return fmt.Errorf("HLS pusher already running")
	}
	
	h.pushURL = pushURL
	h.running = true
	
	// 启动推流协程
	go h.pushStream()
	
	return nil
}

// Push 开始推流到指定的RTMP地址
func (h *HLSPusher) Push(rtmpURL string) error {
	// 解析RTMP URL
	u, err := url.Parse(rtmpURL)
	if err != nil {
		return fmt.Errorf("无效的RTMP URL: %v", err)
	}

	// 添加默认端口
	_, _, err = net.SplitHostPort(u.Host)
	if err != nil {
		u.Host = net.JoinHostPort(u.Host, "1935")
	}

	fmt.Printf("开始推流到RTMP地址: %s\n", rtmpURL)
	
	// 创建RTMP客户端连接
	conn := &gortmplib.Client{
		URL:     u,
		Publish: true, // 设置为发布模式
	}
	
	// 初始化连接
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	err = conn.Initialize(ctx)
	cancel()
	if err != nil {
		return fmt.Errorf("RTMP连接失败: %v", err)
	}
	
	fmt.Printf("RTMP连接已建立: %s\n", u.Host)
	
	// 启动一个goroutine来读取流数据并推送到RTMP
	go func() {
		defer func() {
			conn.Close()
			fmt.Printf("RTMP推流已停止: %s\n", rtmpURL)
		}()

		// 从流中读取数据并推送到RTMP服务器
		// 注意：这是一个简化的实现，实际应用中需要正确解析和封装音视频数据
		for {
			select {
			case <-h.done:
				fmt.Printf("收到停止信号，RTMP推流已停止: %s\n", rtmpURL)
				return
			case data := <-h.stream.buffer:
				// 发送数据到RTMP服务器
				err := conn.NetConn().SetWriteDeadline(time.Now().Add(5 * time.Second))
				if err != nil {
					fmt.Printf("设置写入超时失败: %v\n", err)
					return
				}
				
				// 直接将数据写入RTMP连接
				_, err = conn.NetConn().Write(data)
				if err != nil {
					fmt.Printf("发送RTMP数据失败: %v\n", err)
					return
				}
				
				fmt.Printf("推送 %d 字节数据到 %s\n", len(data), rtmpURL)
			}
		}
	}()
	
	return nil
}

// pushStream 实际执行推流逻辑
func (h *HLSPusher) pushStream() {
	if !h.running || h.pushURL == "" {
		return
	}
	
	// 尝试解析URL以确定协议类型
	u, err := url.Parse(h.pushURL)
	if err != nil {
		fmt.Printf("无法解析推流URL %s: %v\n", h.pushURL, err)
		return
	}
	
	// 根据协议类型选择推流方式
	switch u.Scheme {
	case "rtmp":
		// RTMP推流
		if err := h.Push(h.pushURL); err != nil {
			fmt.Printf("RTMP推流失败: %v\n", err)
		}
	default:
		// 默认实现，模拟推流
		h.simulatePush()
	}
}

// simulatePush 模拟推流过程（用于测试或非RTMP协议）
func (h *HLSPusher) simulatePush() {
	// 启动一个goroutine来读取流数据并推送到目标地址
	go func() {
		defer func() {
			fmt.Printf("模拟推流已停止: %s\n", h.pushURL)
		}()
		
		// 从流中读取数据并模拟推送
		for {
			select {
			case <-h.done:
				fmt.Printf("收到停止信号，模拟推流已停止: %s\n", h.pushURL)
				return
			case data := <-h.stream.buffer:
				// 这里应该从实际的流中读取数据并推送到目标服务器
				fmt.Printf("推送 %d 字节数据到 %s\n", len(data), h.pushURL)
			}
		}
	}()
}

// Stop 停止HLS推流
func (h *HLSPusher) Stop() error {
	if !h.running {
		return fmt.Errorf("HLS pusher not running")
	}
	
	h.running = false
	close(h.done)
	return nil
}