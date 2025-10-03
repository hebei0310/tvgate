package publisher

import (
	"encoding/json"
	"net/http"
	"fmt"
	"strings"
	"sync"
)

// HTTPHandler 推流HTTP处理器
type HTTPHandler struct {
	publisher *StreamPublisher
	mutex     sync.RWMutex
}

// StreamInfo 流信息
type StreamInfo struct {
	ID            string            `json:"id"`
	Source        SourceConfig      `json:"source"`
	LocalPlayURLs PlayURLs          `json:"local_play_urls"`
	Mode          string            `json:"mode"`
	Clients       []ClientInfo      `json:"clients"`
}

// ClientInfo 客户端信息
type ClientInfo struct {
	ID       string   `json:"id"`
	PushURL  string   `json:"push_url"`
	PlayURLs PlayURLs `json:"play_urls"`
	IsPrimary bool    `json:"is_primary"`
}

// NewHTTPHandler 创建新的HTTP处理器
func NewHTTPHandler(p *StreamPublisher) *HTTPHandler {
	return &HTTPHandler{
		publisher: p,
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

// ServeHTTP 实现HTTP处理接口
func (h *HTTPHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// 获取publisher路径前缀
	prefix := "/publisher"
	pub := h.getPublisher()
	if pub != nil && pub.config != nil {
		prefix = pub.config.Path
	}
	
	// 检查请求路径是否匹配publisher路径前缀
	if !strings.HasPrefix(r.URL.Path, prefix) {
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}
	
	// 检查publisher是否已初始化
	if pub == nil {
		http.Error(w, "Publisher not initialized", http.StatusServiceUnavailable)
		return
	}
	
	// 移除路径前缀以获取剩余路径
	path := strings.TrimPrefix(r.URL.Path, prefix)
	
	// 检查是否是本地播放请求 /publisher/play/{streamID}
	if strings.HasPrefix(path, "/play/") {
		streamID := strings.TrimPrefix(path, "/play/")
		h.handleLocalPlay(w, r, streamID, pub)
		return
	}
	
	// 根据不同的请求方法和路径处理
	switch r.Method {
	case "GET":
		h.handleGet(w, r, path, pub)
	case "POST":
		h.handlePost(w, r, path, pub)
	case "DELETE":
		h.handleDelete(w, r, path, pub)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *HTTPHandler) handleLocalPlay(w http.ResponseWriter, r *http.Request, streamID string, pub *StreamPublisher) {
	stream := pub.GetStream(streamID)
	if stream == nil {
		http.Error(w, "Stream not found", http.StatusNotFound)
		return
	}
	
	// 处理本地播放
	stream.HandleLocalPlay(w, r)
}

func (h *HTTPHandler) handleGet(w http.ResponseWriter, r *http.Request, path string, pub *StreamPublisher) {
	path = strings.TrimPrefix(path, "/")
	
	if path == "" {
		// 返回所有流的信息
		h.listStreams(w, r, pub)
	} else {
		// 返回特定流的信息
		h.getStream(w, r, path, pub)
	}
}

func (h *HTTPHandler) handlePost(w http.ResponseWriter, r *http.Request, path string, pub *StreamPublisher) {
	path = strings.TrimPrefix(path, "/")
	
	if path == "" {
		// 创建新流
		h.createStream(w, r, pub)
	} else {
		// 控制特定流
		h.controlStream(w, r, path, pub)
	}
}

func (h *HTTPHandler) handleDelete(w http.ResponseWriter, r *http.Request, path string, pub *StreamPublisher) {
	path = strings.TrimPrefix(path, "/")
	
	if path == "" {
		http.Error(w, "Stream ID required", http.StatusBadRequest)
		return
	}
	
	h.deleteStream(w, r, path, pub)
}

// listStreams 列出所有流
func (h *HTTPHandler) listStreams(w http.ResponseWriter, r *http.Request, pub *StreamPublisher) {
	streams := pub.ListStreams()
	
	streamInfos := make([]StreamInfo, 0, len(streams))
	for _, stream := range streams {
		clients := stream.GetClients()
		clientInfos := make([]ClientInfo, 0, len(clients))
		for _, client := range clients {
			clientInfos = append(clientInfos, ClientInfo{
				ID:        client.id,
				PushURL:   client.pushURL,
				PlayURLs:  client.playURLs,
				IsPrimary: client.isPrimary,
			})
		}
		
		streamInfos = append(streamInfos, StreamInfo{
			ID:            stream.id,
			Source:        stream.config.Stream.Source,
			LocalPlayURLs: stream.localPlayURLs,
			Mode:          stream.config.Stream.Mode,
			Clients:       clientInfos,
		})
	}
	
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(streamInfos)
}

// getStream 获取特定流信息
func (h *HTTPHandler) getStream(w http.ResponseWriter, r *http.Request, streamID string, pub *StreamPublisher) {
	stream := pub.GetStream(streamID)
	if stream == nil {
		http.Error(w, "Stream not found", http.StatusNotFound)
		return
	}
	
	clients := stream.GetClients()
	clientInfos := make([]ClientInfo, 0, len(clients))
	for _, client := range clients {
		clientInfos = append(clientInfos, ClientInfo{
			ID:        client.id,
			PushURL:   client.pushURL,
			PlayURLs:  client.playURLs,
			IsPrimary: client.isPrimary,
		})
	}
	
	streamInfo := StreamInfo{
		ID:            stream.id,
		Source:        stream.config.Stream.Source,
		LocalPlayURLs: stream.localPlayURLs,
		Mode:          stream.config.Stream.Mode,
		Clients:       clientInfos,
	}
	
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(streamInfo)
}

// createStream 创建新流
func (h *HTTPHandler) createStream(w http.ResponseWriter, r *http.Request, pub *StreamPublisher) {
	// 解析请求体中的流配置
	var streamConfig StreamConfig
	if err := json.NewDecoder(r.Body).Decode(&streamConfig); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}
	
	// 获取流ID（可以从查询参数或请求头中获取）
	streamID := r.URL.Query().Get("id")
	if streamID == "" {
		http.Error(w, "Stream ID required", http.StatusBadRequest)
		return
	}
	
	// 创建一个临时配置对象来传递给AddStream
	tempConfig := &StreamItemConfig{
		Stream: streamConfig,
	}
	
	// 添加流到publisher
	pub.AddStream(streamID, tempConfig, r)
	
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	fmt.Fprintf(w, `{"message": "Stream %s created successfully"}`, streamID)
}

// controlStream 控制特定流
func (h *HTTPHandler) controlStream(w http.ResponseWriter, r *http.Request, streamID string, pub *StreamPublisher) {
	stream := pub.GetStream(streamID)
	if stream == nil {
		http.Error(w, "Stream not found", http.StatusNotFound)
		return
	}
	
	// 解析控制命令
	r.ParseForm()
	action := r.FormValue("action")
	
	switch action {
	case "start":
		stream.Start()
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("Stream started"))
	case "stop":
		stream.Stop()
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("Stream stopped"))
	case "push":
		// 模拟推送数据
		data := r.FormValue("data")
		if data != "" {
			stream.PushData([]byte(data))
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("Data pushed"))
		} else {
			http.Error(w, "No data provided", http.StatusBadRequest)
		}
	default:
		http.Error(w, "Invalid action", http.StatusBadRequest)
	}
}

// deleteStream 删除特定流
func (h *HTTPHandler) deleteStream(w http.ResponseWriter, r *http.Request, streamID string, pub *StreamPublisher) {
	pub.RemoveStream(streamID)
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"message": "Stream deleted", "stream_id": "%s"}`, streamID)
}