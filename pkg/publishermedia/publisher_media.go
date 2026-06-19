// Package publishermedia 定义媒体通话信令中的 publisher_media，与客户端 Rust `PublisherMedia` / QUIC 一致。
package publishermedia

// PublisherMedia 发布端采集/解码侧统一参数（HTTP、QUIC、Redis 存取）。
type PublisherMedia struct {
	Width    uint64 `json:"width" msgpack:"width"`
	Height   uint64 `json:"height" msgpack:"height"`
	Fps      uint64 `json:"fps" msgpack:"fps"`
	Bitrate  uint64 `json:"bitrate" msgpack:"bitrate"`
}

// Valid 用于服务端校验请求体是否携带有效参数（与客户端数值范围对齐）。
func (p PublisherMedia) Valid() bool {
	return p.Width >= 160 && p.Width <= 3840 &&
		p.Height >= 120 && p.Height <= 2160 &&
		p.Fps >= 1 && p.Fps <= 60 &&
		p.Bitrate >= 100_000 && p.Bitrate <= 12_000_000
}
