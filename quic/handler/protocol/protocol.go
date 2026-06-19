package protocol

type MessageProtocol interface {
	AddData(chunk []byte)
	TryParse() []byte
	Encode(data []byte) ([]byte, error)
}
