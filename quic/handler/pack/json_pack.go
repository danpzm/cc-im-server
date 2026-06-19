package pack

import "encoding/json"

type JsonPack struct{}

func NewJsonPack() MessagePack {
	return &JsonPack{}
}
func (p *JsonPack) Encode(data any) ([]byte, error) {
	return json.Marshal(data)
}

func (p *JsonPack) Decode(data []byte, v any) error {
	return json.Unmarshal(data, v)
}
