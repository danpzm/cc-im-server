package pack

import "github.com/vmihailenco/msgpack/v5"

type RmpPack struct{}

func NewRmpPack() MessagePack {
	return &RmpPack{}
}
func (p *RmpPack) Encode(data any) ([]byte, error) {
	return msgpack.Marshal(data)
}

func (p *RmpPack) Decode(data []byte, v any) error {
	return msgpack.Unmarshal(data, v)
}
