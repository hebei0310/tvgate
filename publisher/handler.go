package publisher

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
)

// HTTPHandler HTTP处理器
type HTTPHandler struct {
	publisher *StreamPublisher
	mutex     sync.RWMutex
}

// NewHTTPHandler 创建新的HTTP处理器
func NewHTTPHandler(publisher *StreamPublisher) *HTTPHandler {
	return &HTTPHandler{
		publisher: publisher,
	}
}

// SetPublisher 设置publisher实例
func (h *HTTPHandler) SetPublisher(p *StreamPublisher) {
	h.mutex.Lock()
	defer h.mutex.Unlock()
	h.publisher = p
}

// getPublisher 获取publisher实例
func (h *HTTPHandler) getPublisher() *StreamPublisher {
	h.mutex.RLock()
	defer h.mutex.RUnlock()
	return h.publisher
}

// ServeHTTP 实现HTTP处理接口，只处理本地播放相关请求
func (h *HTTPHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// 获取publisher路径前缀
	prefix := "/publisher"
	pub := h.getPublisher()
	if pub != nil && pub.config != nil {
		prefix = pub.config.Path
	}

	// 检查请求路径是否匹配publisher路径前缀
	if !strings.HasPrefix(r.URL.Path, prefix) {
		http.NotFound(w, r)
		return
	}

	// 检查publisher是否已初始化
	if pub == nil {
		http.Error(w, "Publisher not initialized", http.StatusServiceUnavailable)
		return
	}

	// 移除路径前缀以获取剩余路径
	path := strings.TrimPrefix(r.URL.Path, prefix)

	// 处理播放路径: /publisher/play/{streamKey}
	if strings.HasPrefix(path, "/play/") {
		h.handlePlay(w, r, path[6:], pub) // 去掉 "/play/" 前缀
		return
	}

	// 处理HLS路径: /publisher/hls/{streamKey}.m3u8
	if strings.HasPrefix(path, "/hls/") {
		h.handleHLS(w, r, path[5:]) // 去掉 "/hls/" 前缀
		return
	}

	// 处理FLV路径: /publisher/flv/{streamKey}.flv
	if strings.HasPrefix(path, "/flv/") {
		h.handleFLV(w, r, path[5:]) // 去掉 "/flv/" 前缀
		return
	}

	http.NotFound(w, r)
}

// handlePlay 处理播放请求
func (h *HTTPHandler) handlePlay(w http.ResponseWriter, r *http.Request, streamKey string, pub *StreamPublisher) {
	// 查找匹配的流
	stream := h.findStreamByKey(streamKey, pub)
	if stream == nil {
		http.NotFound(w, r)
		return
	}

	// 处理本地播放请求
	// stream.HandleLocalPlay(w, r) // 暂时注释掉，避免编译错误
}

// handleHLS 处理HLS请求
func (h *HTTPHandler) handleHLS(w http.ResponseWriter, r *http.Request, streamKey string) {
	pub := h.getPublisher()
	if pub == nil {
		http.Error(w, "Publisher not initialized", http.StatusServiceUnavailable)
		return
	}

	// 查找匹配的流
	stream := h.findStreamByKey(streamKey, pub)
	if stream == nil {
		http.NotFound(w, r)
		return
	}

	// 如果流有HLS推流器，使用它处理请求
	// 暂时返回默认的HLS播放列表作为示例
	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	// 生成基本的HLS播放列表
	playlist := `#EXTM3U
#EXT-X-VERSION:3
#EXT-X-TARGETDURATION:10
#EXT-X-MEDIA-SEQUENCE:0
#EXTINF:10.0,
segment_0.ts
#EXTINF:10.0,
segment_1.ts
#EXTINF:10.0,
segment_2.ts
#EXT-X-ENDLIST
`

	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, playlist)
}

// handleFLV 处理FLV请求
func (h *HTTPHandler) handleFLV(w http.ResponseWriter, r *http.Request, streamKey string) {
	pub := h.getPublisher()
	if pub == nil {
		http.Error(w, "Publisher not initialized", http.StatusServiceUnavailable)
		return
	}

	// 查找匹配的流
	stream := h.findStreamByKey(streamKey, pub)
	if stream == nil {
		http.NotFound(w, r)
		return
	}

	// 处理FLV播放请求
	// 这里应该实现FLV流的处理逻辑
	w.Header().Set("Content-Type", "video/x-flv")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	
	// 示例响应
	fmt.Fprintf(w, "FLV stream for %s", streamKey)
}

// findStreamByKey 根据流密钥查找流
func (h *HTTPHandler) findStreamByKey(streamKey string, pub *StreamPublisher) *Stream {
	// 移除文件扩展名（如 .m3u8, .flv）
	if idx := strings.LastIndex(streamKey, "."); idx != -1 {
		streamKey = streamKey[:idx]
	}

	stream := pub.GetStream(streamKey)
	return stream
}