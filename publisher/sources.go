package publisher

import (
	"bufio"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/bluenviron/gortsplib/v5"
	"github.com/bluenviron/gortsplib/v5/pkg/base"
	"github.com/bluenviron/gortsplib/v5/pkg/description"
	"github.com/bluenviron/gortsplib/v5/pkg/format"
	"github.com/pion/rtp"
)

type Packet struct {
	Data []byte
}

type StreamSource interface {
	Packets() <-chan Packet
	Close()
}

// -------------------- RTSP --------------------
type RTSPSource struct {
	URL      string
	Backup   string
	packetCh chan Packet
	closed   chan struct{}
}

func NewRTSPSource(url, backup string) StreamSource {
	r := &RTSPSource{
		URL:      url,
		Backup:   backup,
		packetCh: make(chan Packet, 1024),
		closed:   make(chan struct{}),
	}
	go r.loop()
	return r
}

func (r *RTSPSource) loop() {
	for {
		select {
		case <-r.closed:
			close(r.packetCh)
			return
		default:
		}

		func() {
			// 创建RTSP客户端
			client := &gortsplib.Client{
				Protocol: func() *gortsplib.Protocol {
					t := gortsplib.ProtocolTCP
					return &t
				}(),
			}

			// 连接到服务器
			err := client.Start()
			if err != nil {
				log.Printf("RTSP connect failed: %v", err)
				time.Sleep(2 * time.Second)
				if r.Backup != "" {
					r.URL, r.Backup = r.Backup, r.URL
				}
				return
			}
			defer client.Close()

			// 获取描述信息
			parsedURL, err := base.ParseURL(r.URL)
			if err != nil {
				log.Printf("RTSP parse URL failed: %v", err)
				return
			}

			_, err = client.Options(parsedURL)
			if err != nil {
				log.Printf("RTSP OPTIONS failed: %v", err)
				return
			}

			desc, _, err := client.Describe(parsedURL)
			if err != nil {
				log.Printf("RTSP describe failed: %v", err)
				return
			}

			// 设置媒体
			var medias []*description.Media
			for _, m := range desc.Medias {
				_, err := client.Setup(parsedURL, m, 0, 0)
				if err != nil {
					log.Printf("RTSP setup failed for media: %v", err)
					continue
				}
				medias = append(medias, m)
			}

			if len(medias) == 0 {
				log.Printf("No supported media found in RTSP stream")
				return
			}

			// 处理收到的RTP包
			client.OnPacketRTPAny(func(medi *description.Media, forma format.Format, pkt *rtp.Packet) {
				// 将RTP包编码为字节流
				encoded, err := pkt.Marshal()
				if err != nil {
					log.Printf("Failed to marshal RTP packet: %v", err)
					return
				}
				
				// 发送到包通道
				select {
				case r.packetCh <- Packet{Data: encoded}:
				case <-r.closed:
					return
				default:
					// 丢弃数据包以防止阻塞
				}
			})

			// 开始拉流
			_, err = client.Play(nil)
			if err != nil {
				log.Printf("RTSP play failed: %v", err)
				return
			}

			// 等待流结束或被关闭
			client.Wait()
		}()
	}
}

func (r *RTSPSource) Packets() <-chan Packet {
	return r.packetCh
}

func (r *RTSPSource) Close() {
	close(r.closed)
}

// -------------------- HTTP/HLS --------------------
type HTTPSource struct {
	URL      string
	packetCh chan Packet
	closed   chan struct{}
}

func NewHTTPSource(url string) StreamSource {
	h := &HTTPSource{
		URL:      url,
		packetCh: make(chan Packet, 1024),
		closed:   make(chan struct{}),
	}
	go h.loop()
	return h
}

func (h *HTTPSource) loop() {
	for {
		select {
		case <-h.closed:
			close(h.packetCh)
			return
		default:
		}

		// 检查是否为M3U8播放列表
		if strings.Contains(h.URL, ".m3u8") {
			h.handleHLSStream()
		} else {
			h.handleRegularHTTPStream()
		}
		
		log.Printf("HTTP stream ended, reconnecting...")
		select {
		case <-h.closed:
			close(h.packetCh)
			return
		case <-time.After(2 * time.Second):
		}
	}
}

func (h *HTTPSource) handleRegularHTTPStream() {
	// 创建带超时的HTTP客户端
	client := &http.Client{
		Timeout: 30 * time.Second,
	}

	// 创建请求并添加常见头部
	req, err := http.NewRequest("GET", h.URL, nil)
	if err != nil {
		log.Printf("HTTP request creation error: %v", err)
		return
	}

	// 添加常见的流媒体请求头
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/91.0.4472.124 Safari/537.36")
	req.Header.Set("Connection", "keep-alive")
	req.Header.Set("Accept", "*/*")

	resp, err := client.Do(req)
	if err != nil {
		log.Printf("HTTP get error: %v", err)
		return
	}

	// 检查响应状态码
	if resp.StatusCode != http.StatusOK {
		log.Printf("HTTP response status error: %d %s", resp.StatusCode, resp.Status)
		resp.Body.Close()
		return
	}

	log.Printf("HTTP stream connected: %s", h.URL)
	reader := bufio.NewReader(resp.Body)
	buf := make([]byte, 4096)
	totalBytes := 0
	emptyReads := 0
	maxEmptyReads := 10 // 最大空读取次数

	for {
		// 注意：io.ReadCloser没有SetReadDeadline方法，所以我们不能设置读取超时
		
		n, err := reader.Read(buf)
		if err != nil {
			if err == io.EOF {
				log.Printf("HTTP stream ended (EOF), total bytes read: %d", totalBytes)
			} else {
				log.Printf("HTTP read error: %v", err)
			}
			resp.Body.Close()
			break
		}

		// 检查是否读取到数据
		if n == 0 {
			emptyReads++
			if emptyReads > maxEmptyReads {
				log.Printf("Too many empty reads, closing connection")
				resp.Body.Close()
				break
			}
			continue
		} else {
			emptyReads = 0 // 重置空读取计数
		}

		totalBytes += n
		select {
		case h.packetCh <- Packet{Data: buf[:n]}:
		case <-h.closed:
			resp.Body.Close()
			return
		}
		
		// 添加日志以便调试，但不要过于频繁
		if totalBytes%(4096*25) == 0 { // 每100KB打印一次日志
			log.Printf("HTTP stream read %d KB", totalBytes/1024)
		}
	}
}

func (h *HTTPSource) handleHLSStream() {
	// 处理HLS流，需要解析M3U8并获取实际的TS片段
	log.Printf("Handling HLS stream: %s", h.URL)
	
	// 创建带超时的HTTP客户端
	client := &http.Client{
		Timeout: 30 * time.Second,
	}
	
	processedSegments := make(map[string]time.Time) // 记录已处理的片段和处理时间
	cleanupInterval := 10 * time.Minute              // 清理间隔
	
	for {
		// 检查是否应该关闭
		select {
		case <-h.closed:
			return
		default:
		}
		
		// 定期清理处理记录，避免内存泄漏
		if time.Now().Sub(getOldestSegmentTime(processedSegments)) > cleanupInterval {
			cleanupOldSegments(processedSegments, cleanupInterval)
		}
		
		// 获取M3U8播放列表
		resp, err := client.Get(h.URL)
		if err != nil {
			log.Printf("Failed to fetch M3U8 playlist: %v", err)
			time.Sleep(2 * time.Second)
			continue
		}
		
		if resp.StatusCode != http.StatusOK {
			log.Printf("M3U8 playlist fetch error: %d %s", resp.StatusCode, resp.Status)
			resp.Body.Close()
			time.Sleep(2 * time.Second)
			continue
		}
		
		// 读取播放列表内容
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			log.Printf("Failed to read M3U8 playlist: %v", err)
			time.Sleep(2 * time.Second)
			continue
		}
		
		// 解析M3U8内容，查找TS片段URL
		lines := strings.Split(string(body), "\n")
		baseURL := h.getBaseURL(h.URL)
		
		// 遍历播放列表行查找TS片段
		newSegments := 0
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			
			// 检查是否应该关闭
			select {
			case <-h.closed:
				return
			default:
			}
			
			// 构造完整的TS片段URL
			tsURL := line
			if !strings.HasPrefix(line, "http") {
				tsURL = baseURL + line
			}
			
			// 检查片段是否已经处理过
			if _, exists := processedSegments[tsURL]; exists {
				continue
			}
			
			// 获取TS片段内容
			tsResp, err := client.Get(tsURL)
			if err != nil {
				log.Printf("Failed to fetch TS segment: %v", err)
				continue
			}
			
			if tsResp.StatusCode != http.StatusOK {
				log.Printf("TS segment fetch error: %d %s", tsResp.StatusCode, tsResp.Status)
				tsResp.Body.Close()
				continue
			}
			
			// 读取并发送TS片段数据
			buf := make([]byte, 4096)
			totalBytes := 0
			for {
				n, err := tsResp.Body.Read(buf)
				if err != nil && err != io.EOF {
					log.Printf("TS segment read error: %v", err)
					break
				}
				
				if n > 0 {
					totalBytes += n
					select {
					case h.packetCh <- Packet{Data: buf[:n]}:
						// 数据已发送
					case <-h.closed:
						tsResp.Body.Close()
						return
					}
				}
				
				if err == io.EOF {
					break
				}
			}
			
			tsResp.Body.Close()
			log.Printf("Sent TS segment (%d bytes): %s", totalBytes, tsURL)
			processedSegments[tsURL] = time.Now()
			newSegments++
		}
		
		log.Printf("Processed %d new TS segments", newSegments)
		
		// 等待一段时间再重新获取播放列表
		select {
		case <-h.closed:
			return
		case <-time.After(2 * time.Second):
		}
	}
}

// getOldestSegmentTime 获取处理片段中最旧的时间
func getOldestSegmentTime(segments map[string]time.Time) time.Time {
	oldest := time.Now()
	for _, t := range segments {
		if t.Before(oldest) {
			oldest = t
		}
	}
	return oldest
}

// cleanupOldSegments 清理旧的片段记录
func cleanupOldSegments(segments map[string]time.Time, interval time.Duration) {
	threshold := time.Now().Add(-interval)
	for url, t := range segments {
		if t.Before(threshold) {
			delete(segments, url)
		}
	}
}

func (h *HTTPSource) getBaseURL(url string) string {
	// 简单解析URL以获取基本路径
	parts := strings.Split(url, "/")
	if len(parts) < 3 {
		return ""
	}
	return strings.Join(parts[:len(parts)-1], "/") + "/"
}

func (h *HTTPSource) Packets() <-chan Packet {
	return h.packetCh
}

func (h *HTTPSource) Close() {
	close(h.closed)
}

// -------------------- 本地文件 --------------------
type FileSource struct {
	Path     string
	packetCh chan Packet
	closed   chan struct{}
}

func NewFileSource(path string) StreamSource {
	f := &FileSource{
		Path:     path,
		packetCh: make(chan Packet, 1024),
		closed:   make(chan struct{}),
	}
	go f.loop()
	return f
}

func (f *FileSource) loop() {
	file, err := os.Open(f.Path)
	if err != nil {
		log.Printf("File open error: %v", err)
		close(f.packetCh)
		return
	}
	defer file.Close()

	buf := make([]byte, 4096)
	for {
		select {
		case <-f.closed:
			close(f.packetCh)
			return
		default:
		}

		n, err := file.Read(buf)
		if err != nil {
			if err != io.EOF {
				log.Printf("File read error: %v", err)
			}
			time.Sleep(time.Second)
			continue
		}
		f.packetCh <- Packet{Data: buf[:n]}
	}
}

func (f *FileSource) Packets() <-chan Packet {
	return f.packetCh
}

func (f *FileSource) Close() {
	close(f.closed)
}