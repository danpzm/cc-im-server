package pack

type MessagePack interface {
	Encode(data any) ([]byte, error)
	Decode(data []byte, v any) error
}
