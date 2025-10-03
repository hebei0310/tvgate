package publisher

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
	
	"github.com/qist/tvgate/logger"
)

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
	case "file":
		s.pullFileStream()
	case "udp":
		s.pullUDPStream()
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
	// TODO: 实现完整的RTMP拉流逻辑
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

// pullFileStream 从本地文件拉流
func (s *Stream) pullFileStream() {
	// 本地文件流拉取逻辑
	logger.LogPrintf("Pulling file stream from %s", s.config.Stream.Source.URL)
	
	// 解析文件路径
	filePath := strings.TrimPrefix(s.config.Stream.Source.URL, "file://")
	
	// 打开文件
	file, err := os.Open(filePath)
	if err != nil {
		logger.LogPrintf("Failed to open file %s for stream %s: %v", filePath, s.id, err)
		return
	}
	defer file.Close()
	
	// 读取并转发数据
	buffer := make([]byte, 4096)
	for {
		select {
		case <-s.done:
			return
		default:
			n, err := file.Read(buffer)
			if err != nil && err != io.EOF {
				logger.LogPrintf("Failed to read file stream %s: %v", s.id, err)
				return
			}
			
			if n > 0 {
				// 将数据推送到缓冲区
				s.PushData(buffer[:n])
			}
			
			if err == io.EOF {
				// 文件读取完毕，循环播放
				_, err = file.Seek(0, 0)
				if err != nil {
					logger.LogPrintf("Failed to seek file stream %s: %v", s.id, err)
					return
				}
				// 继续读取
			}
		}
	}
}

// pullUDPStream 从UDP流拉流
func (s *Stream) pullUDPStream() {
	// UDP流拉取逻辑
	logger.LogPrintf("Pulling UDP stream from %s", s.config.Stream.Source.URL)
	
	// 解析UDP地址
	udpAddr, err := net.ResolveUDPAddr("udp", s.config.Stream.Source.URL)
	if err != nil {
		logger.LogPrintf("Failed to resolve UDP address %s for stream %s: %v", s.config.Stream.Source.URL, s.id, err)
		return
	}
	
	// 创建UDP连接
	conn, err := net.DialUDP("udp", nil, udpAddr)
	if err != nil {
		logger.LogPrintf("Failed to create UDP connection for stream %s: %v", s.id, err)
		return
	}
	defer conn.Close()
	
	// 读取并转发数据
	buffer := make([]byte, 4096)
	for {
		select {
		case <-s.done:
			return
		default:
			n, _, err := conn.ReadFromUDP(buffer)
			if err != nil {
				logger.LogPrintf("Failed to read UDP stream %s: %v", s.id, err)
				return
			}
			
			if n > 0 {
				// 将数据推送到缓冲区
				s.PushData(buffer[:n])
			}
		}
	}
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