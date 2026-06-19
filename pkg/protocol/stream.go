package protocol

import "fmt"

// StreamCategory 表示流的大类
type StreamCategory uint8

const (
	VERSION                        = 1
	CategoryUnknown StreamCategory = 0
	CategoryControl StreamCategory = 1 // 控制类流（认证、心跳等）
	CategoryData    StreamCategory = 2 // 数据类流（消息、文件等）
)

// StreamType 表示具体的流类型
type StreamType struct {
	Category StreamCategory
	SubType  uint8 // 子类型
	Version  uint8 // 版本号，用于协议升级
}

// 预定义的流类型常量
var (
	// 控制类流
	StreamTypeAuth = StreamType{
		Category: CategoryControl,
		SubType:  1,
		Version:  VERSION,
	}
	StreamTypeHeartbeat = StreamType{
		Category: CategoryControl,
		SubType:  2,
		Version:  VERSION,
	}
	StreamTypeMessage = StreamType{
		Category: CategoryData,
		SubType:  1,
		Version:  VERSION,
	}
)

// String 方法用于打印和日志
func (s StreamType) String() string {
	return fmt.Sprintf("StreamType{Category: %d, SubType: %d, Version: %d}",
		s.Category, s.SubType, s.Version)
}

// ToBytes 将StreamType序列化为字节数组
func (s StreamType) ToBytes() []byte {
	return []byte{byte(s.Category), s.SubType, s.Version}
}

// FromBytes 从字节数组反序列化StreamType
func FromBytes(data []byte) (StreamType, error) {
	if len(data) < 3 {
		return StreamType{}, fmt.Errorf("invalid stream type data length")
	}
	return StreamType{
		Category: StreamCategory(data[0]),
		SubType:  data[1],
		Version:  data[2],
	}, nil
}

// Equal 比较两个StreamType是否相等
func (s StreamType) Equal(other StreamType) bool {
	return s.Category == other.Category &&
		s.SubType == other.SubType &&
		s.Version == other.Version
}
