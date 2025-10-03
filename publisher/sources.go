package publisher

import (
	"bufio"
	"io"
	"log"
	"net/http"
	"os"
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

		resp, err := http.Get(h.URL)
		if err != nil {
			log.Printf("HTTP get error: %v", err)
			time.Sleep(2 * time.Second)
			continue
		}
		reader := bufio.NewReader(resp.Body)
		buf := make([]byte, 4096)
		for {
			n, err := reader.Read(buf)
			if err != nil {
				if err != io.EOF {
					log.Printf("HTTP read error: %v", err)
				}
				resp.Body.Close()
				break
			}
			h.packetCh <- Packet{Data: buf[:n]}
		}
		time.Sleep(time.Second)
	}
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