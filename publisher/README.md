# Publisher Module

Publisher模块负责处理流转发功能，支持将本地流转发到远程地址，同时也支持通过HTTP接口本地播放。

## 功能特性

- 支持多种流源类型（RTSP、HTTP/HLS等）
- 支持流的纯转发（不编码）
- 支持主备模式和全部推送模式
- 支持动态流密钥生成
- 支持流密钥过期和自动重新生成
- 支持本地播放接口（FLV、HLS）
- HTTP接口管理流

## 配置说明

在配置文件中添加publisher配置项:

```yaml
publisher:
  cctv1:
    protocol: "go"
    buffer_size: 100
    enabled: true
    streamkey:
      type: "random"
      value: ""              # 自动生成的流密钥会保存在这里
      length: 16             # 随机密钥长度
      expiration: "24h"      # 过期时间，例如: "24h", "720h"，"0"表示永不过期
      generated: ""          # 流密钥生成时间，自动生成
    stream:
      source:
        type: "rtsp"
        url: "rtsp://192.168.1.10/live/cctv1"
        backup_url: "rtsp://192.168.1.11/live/cctv1"
        headers:
          User-Agent: "okhttp/3.12.0"
          Authorization: "Bearer $(streamkey.value)"
      local_play_urls:
        flv: "http://192.168.100.228/live/"     # 可以配置静态地址，会自动拼接为 http://192.168.100.228/live/[streamkey].flv
        hls: "http://192.168.100.228/live/"     # 可以配置静态地址，会自动拼接为 http://192.168.100.228/live/[streamkey].m3u8
      # 或者不配置local_play_urls，系统会自动生成
      # local_play_urls:
      #   flv: ""  # 系统会自动生成 http(s)://[host]/live/[streamkey].flv
      #   hls: ""  # 系统会自动生成 http(s)://[host]/live/[streamkey].m3u8
      mode: "primary-backup"
      receivers:
        primary:
          push_url: "rtmp://remote-server1.com/live/"  # 会自动拼接为 rtmp://remote-server1.com/live/[streamkey]
          play_urls:
            flv: "http://remote-server1.com/live/"     # 会自动拼接为 http://remote-server1.com/live/[streamkey].flv
            hls: "http://remote-server1.com/live/"     # 会自动拼接为 http://remote-server1.com/live/[streamkey].m3u8
        backup:
          push_url: "rtmp://remote-server2.com/live/$(streamkey.value)?param=value"
          play_urls:
            flv: "http://remote-server2.com/live/$(streamkey.value).flv?param=value"
            hls: "http://remote-server2.com/live/$(streamkey.value).m3u8?param=value"

  cctv2:
    protocol: "ffmpeg"
    buffer_size: 200
    enabled: true
    streamkey:
      type: "fixed"
      value: "my-secret-key-123"
      length: 0
      expiration: "0"        # 固定密钥永不过期
      generated: ""
    stream:
      source:
        type: "http"
        url: "http://origin-server.com/hls/cctv2.m3u8"
        backup_url: "http://backup-server.com/hls/cctv2.m3u8"
        headers:
          User-Agent: "Mozilla/5.0 TVGate"
          Referer: "http://example.com/"
      # local_play_urls 不再需要手动配置，会自动生成
      mode: "all"
      receivers:
        platform1:
          push_url: "rtmp://platform1.com/live/"
          play_urls:
            flv: "http://platform1.com/live/"
            # 如果不配置hls，则不会显示HLS播放地址
        platform2:
          push_url: "rtmp://platform2.com/live/$(streamkey.value)"
          play_urls:
            flv: "http://platform2.com/live/$(streamkey.value).flv"
            hls: "http://platform2.com/live/$(streamkey.value).m3u8"
```

## 配置字段说明

### Publisher配置
- `protocol`: 推流协议 ("go" 或 "ffmpeg")
- `buffer_size`: 缓冲区大小
- `enabled`: 是否启用该publisher

### StreamKey配置
- `type`: 流密钥类型 ("random" 或 "fixed")
- `value`: 固定密钥值（type为fixed时使用）或自动生成的随机密钥
- `length`: 随机密钥长度（type为random时使用）
- `expiration`: 过期时间（例如: "24h", "720h"，"0"表示永不过期）
- `generated`: 流密钥生成时间（自动生成，不需要手动配置）

### Stream配置
- `source`: 源配置
  - `type`: 源类型 ("rtsp", "http", "hls")
  - `url`: 主源地址
  - `backup_url`: 备用源地址
  - `headers`: 自定义HTTP请求头（支持占位符替换）
- `local_play_urls`: 本地播放地址（支持配置静态地址或自动生成）
  - `flv`: 本地FLV播放地址（支持以"/"结尾自动拼接streamkey.flv）
  - `hls`: 本地HLS播放地址（支持以"/"结尾自动拼接streamkey.m3u8）
- `mode`: 推送模式 ("primary-backup" 或 "all")
- `receivers`: 接收方配置（map类型）
  - `push_url`: 推流地址（支持以"/"结尾自动拼接streamkey）
  - `play_urls`: 接收方播放地址
    - `flv`: FLV播放地址（支持以"/"结尾自动拼接streamkey.flv）
    - `hls`: HLS播放地址（支持以"/"结尾自动拼接streamkey.m3u8）

## API接口

### HTTP接口

- `GET /publisher` - 获取所有流列表
- `GET /publisher/{streamID}` - 获取特定流信息
- `POST /publisher?id={streamID}` - 创建新流
- `POST /publisher/{streamID}` - 控制流 (action=start|stop|push)
- `DELETE /publisher/{streamID}` - 删除流
- `GET /publisher/play/{streamID}` - 本地播放流

## 使用说明

1. 在配置文件中启用publisher模块并配置流
2. 启动服务
3. 流将自动从源地址拉取并转发到配置的目标地址
4. 可以通过`/publisher/play/{streamID}`本地播放流
5. 可以通过HTTP接口管理流

## 工作原理

1. 系统启动时，如果配置了随机流密钥且未过期，则使用现有密钥
2. 如果流密钥过期或未配置，系统会自动生成新的流密钥并保存到配置中
3. 系统每分钟检查一次流密钥是否过期
4. 如果流密钥过期，系统会：
   - 停止当前流
   - 重新生成流密钥
   - 使用新密钥重新配置并启动流
   - 更新所有相关的URL（推流地址、播放地址等）

## 架构设计

Publisher模块采用以下组件:

- `StreamPublisher` - 核心转发器，管理所有流
- `Stream` - 单个流，管理转发客户端
- `Client` - 转发客户端，代表一个目标地址连接
- `Config` - 配置管理
- `HTTPHandler` - HTTP接口处理器

数据流:
1. Stream从源地址拉取数据
2. Stream将数据转发给所有Client
3. Client将数据推送到对应的目标地址
4. 用户可以通过HTTP接口本地播放流