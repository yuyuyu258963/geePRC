package codec

import "io"

type Header struct {
	ServiceMethod string // format "Service.Method"
	Seq           uint64 // sequence number chosen by client
	Error         string
}

// 对消息进行编解码的接口Codec
type Codec interface {
	io.Closer
	// 读取出消息中的Header
	ReadHeader(*Header) error
	// 读取出消息中的Body
	ReadBody(interface{}) error
	Write(*Header, interface{}) error
}

type NewCodecFunc func(io.ReadWriteCloser) Codec

type Type string

const (
	GobType  Type = "application/gob"
	JsonType Type = "application/json"
)

// * 设想一下后买就可以请求中的一个Type字段来实例化
// * 对应的编解码器，从而实现对消息的编解码
var NewCodeFuncMap map[Type]NewCodecFunc

func init() { // 编解码方式的注册中心
	NewCodeFuncMap = make(map[Type]NewCodecFunc)
	NewCodeFuncMap[GobType] = NewGobCodec
}
