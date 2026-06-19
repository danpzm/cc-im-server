package protocol

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
)

const NONCE_SIZE = 12

type FourByteProtocol struct {
	buffer     []byte
	headerSize int
	gcm        cipher.AEAD
}

func NewFourByteProtocol(key []byte) MessageProtocol {
	block, err := aes.NewCipher(key)
	if err != nil {
		panic(err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		panic(err)
	}
	return &FourByteProtocol{
		buffer:     make([]byte, 0),
		headerSize: 4,
		gcm:        gcm,
	}
}

// 添加数据到缓冲区（不触发解析）
func (h *FourByteProtocol) AddData(chunk []byte) {
	h.buffer = append(h.buffer, chunk...)
}

// 尝试从缓冲区提取一个完整消息
func (h *FourByteProtocol) TryParse() []byte {
	if len(h.buffer) < h.headerSize {
		return nil
	}

	length := binary.BigEndian.Uint32(h.buffer[:h.headerSize])
	totalLength := int(length) + h.headerSize

	if len(h.buffer) < totalLength {
		return nil
	}

	blob := make([]byte, length)
	copy(blob, h.buffer[h.headerSize:totalLength])
	h.buffer = h.buffer[totalLength:] // 移除已处理数据

	// blob = nonce(12) || ciphertext+tag
	if len(blob) < NONCE_SIZE {
		return []byte{}
	}
	nonce := blob[:NONCE_SIZE]
	ciphertext := blob[NONCE_SIZE:]
	plaintext, err := h.gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return []byte{}
	}
	return plaintext
}

func (h *FourByteProtocol) Encode(bytes []byte) ([]byte, error) {
	nonce := make([]byte, NONCE_SIZE)
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	ciphertext := h.gcm.Seal(nil, nonce, bytes, nil)

	blob := make([]byte, 0, NONCE_SIZE+len(ciphertext))
	blob = append(blob, nonce...)
	blob = append(blob, ciphertext...)

	// 计算消息长度（加密后的blob长度）
	length := uint32(len(blob))

	// 创建一个新的切片，前4个字节用于存储消息长度
	buf := make([]byte, 4+len(blob))
	// 将消息长度写入前4个字节
	binary.BigEndian.PutUint32(buf[:4], length)
	// 将消息内容复制到buf的剩余部分
	copy(buf[4:], blob)
	return buf, nil
}
