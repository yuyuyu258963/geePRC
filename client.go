package geerpc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"geeRPC/codec"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

type Call struct {
	Seq          uint64
	ServerMethod string      // format "<service>.<method>"
	Args         interface{} //arguments to the function
	Reply        interface{} //reply from the function
	Error        error       // if error occurs, it will be set
	Done         chan *Call  // Strobes when call is complete
}

func (call *Call) done() {
	call.Done <- call
}

// Client 是一次服务器连接的抽象
// 一次连接 = 一次协商 + N 次通话
// Client上应该保存 connection、编解码器、此次协商的option
// 还需要用于状态维护的东西，如seq来标识每次通信
type Client struct {
	cc       codec.Codec      // 编解码器
	opt      *Option          // 第一次协商的option
	header   codec.Header     // 连接后信息交换的头部
	sending  sync.Mutex       // protect
	mu       sync.Mutex       //protect
	seq      uint64           // 来标识通信的序号
	pending  map[uint64]*Call // 等待返回的调用
	closing  bool             // user has called Close
	shutdown bool             // server has told us to stop
}

type newClientFunc func(conn net.Conn, opt *Option) (*Client, error)

var _ io.Closer = (*Client)(nil)

var ErrShutdown = errors.New("connection is shutdown")

// close the connection
// 因为一个客户端默认是进行了一次连接所以需要有一个Close方法
func (client *Client) Close() error {
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.closing {
		return ErrShutdown
	}
	client.closing = true
	return client.cc.Close()
}

// IsAvailable return true if the client does work
func (client *Client) IsAvailable() bool {
	client.mu.Lock()
	defer client.mu.Unlock()
	return !client.shutdown && !client.closing
}

// 注册一个调用
func (client *Client) registerCall(call *Call) (uint64, error) {
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.closing || client.shutdown {
		return 0, ErrShutdown
	}
	call.Seq = client.seq
	client.pending[call.Seq] = call
	client.seq++
	return call.Seq, nil
}

// 从等待执行的call中获取一个
func (client *Client) removeCall(seq uint64) *Call {
	client.mu.Lock()
	defer client.mu.Unlock()
	call := client.pending[seq]
	delete(client.pending, seq)
	return call
}

// 终止所有的call
func (client *Client) terminateCalls(err error) {
	client.sending.Lock()
	defer client.sending.Unlock()
	client.mu.Lock()
	defer client.mu.Unlock()
	client.shutdown = true
	// 终止所有在等待的call
	for _, call := range client.pending {
		call.Error = err
		call.done()
	}
}

// Client等待消息的回应，找到对应的call，并且用call。done来通知call已经被执行
func (client *Client) receive() {
	var err error
	for err == nil {
		var h codec.Header
		if err = client.cc.ReadHeader(&h); err != nil {
			break
		}
		call := client.removeCall(h.Seq) // 收到一个回应，那就对应找打对应的call
		switch {
		case call == nil:
			// it usually mens that Writer partially failed
			// and call was already removed
			err = client.cc.ReadBody(nil) // 不需要读取这个回应
		case h.Error != "":
			call.Error = fmt.Errorf(h.Error)
			err = client.cc.ReadBody(nil) //
			call.done()
		default:
			err = client.cc.ReadBody(call.Reply)
			if err != nil {
				call.Error = errors.New("reading body " + err.Error())
			}
			call.done()
		}
	}
	// error occurs, so terminate the client
	client.terminateCalls(err)
}

// NewClient 初始化一个客户端，并协商option
func NewClient(conn net.Conn, opt *Option) (*Client, error) {
	// 先根据option获取到codecType
	f := codec.NewCodeFuncMap[opt.CodecType]
	if f == nil {
		err := fmt.Errorf("invalid codec type %s", opt.CodecType)
		log.Println("rpc client:codec error:", err)
		return nil, err
	}
	// send options with server
	if err := json.NewEncoder(conn).Encode(opt); err != nil {
		log.Println("rpc client: option error:", err)
		_ = conn.Close()
		return nil, err
	}
	return newClientCodec(f(conn), opt), nil
}

// NewHTTPClient new a Client instance via HTTP as transport protocol
func NewHTTPClient(conn net.Conn, opt *Option) (*Client, error) {
	// 就是以HTTP报文的格式来写,其实就是基于TCP后面自己用HTTP的格式来发一次请求
	// 然后server相同的用HTTP的格式来响应请求
	_, _ = io.WriteString(conn, fmt.Sprintf("CONNECT %s HTTP/1.0\n\n", defaultRPCPath))

	// Require successful HTTP response
	// before switching to RPC protocol
	resp, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: "CONNECT"})
	if err == nil && resp.Status == connected {
		// 成功收到请求后托管给RPCClient去处理
		// 然后就是协商Option，创建Client实例了
		return NewClient(conn, opt)
	}
	if err != nil {
		err = errors.New("unexpected HTTP response: " + resp.Status)
	}
	// 返回错误
	return nil, err
}

// 初始化一个服务端
func newClientCodec(cc codec.Codec, opt *Option) *Client {
	client := &Client{
		seq:     1,
		cc:      cc,
		opt:     opt,
		pending: make(map[uint64]*Call),
	}
	go client.receive() // 一旦连接建立，马上开始监听服务端是不是有响应
	return client
}

// 解析作为可选参数的Option
func parseOptions(opts ...*Option) (*Option, error) {
	if len(opts) == 0 || opts[0] == nil {
		return DefaultOption, nil
	}
	if len(opts) != 1 {
		return nil, errors.New("number of options is more than 1")
	}
	opt := opts[0]
	opt.MagicNumber = DefaultOption.MagicNumber
	if opt.CodecType == "" {
		opt.CodecType = DefaultOption.CodecType
	}
	return opt, nil
}

// Dial connects to a RPC server at the specified network address
func Dial(network, addr string, opts ...*Option) (client *Client, err error) {
	return dialWithTimeOut(NewClient, network, addr, opts...)
}

// DialHTTP connects to an HTTP RPC server at the specified network address
// listening on the default HTTP RPC path
func DialHTTP(network, addr string, opts ...*Option) (client *Client, err error) {
	return dialWithTimeOut(NewHTTPClient, network, addr, opts...)
}

// 有建立连接时的超时,建立TCP连接
func dialWithTimeOut(f newClientFunc, network, addr string, opts ...*Option) (client *Client, err error) {
	opt, err := parseOptions(opts...)
	if err != nil {
		return nil, err
	}
	conn, err := net.DialTimeout(network, addr, opt.ConnectionTimeout)
	if err != nil {
		return nil, err
	}
	// close the connection if client is nil
	defer func() {
		if err != nil { // 如果是后面出现了超时则需要关闭connection否则就是没连接成功
			conn.Close()
		}
	}()
	ch := make(chan struct{})
	go func() {
		client, err = f(conn, opt) //因为这里需要协商
		ch <- struct{}{}
	}()
	// 如果设置为0则不超时
	if opt.ConnectionTimeout == 0 {
		<-ch
		return
	}
	select {
	case <-time.After(opt.ConnectionTimeout):
		return nil, fmt.Errorf("rpc client: connect timeout out :expect within %s", opt.ConnectionTimeout)
	case <-ch:
	}
	return
}

func XDial(rpcAddr string, opts ...*Option) (*Client, error) {
	parts := strings.Split(rpcAddr, "@")
	if len(parts) != 2 {
		return nil, fmt.Errorf("rpc client error: %s", rpcAddr)
	}
	protocol, addr := parts[0], parts[1]
	switch protocol {
	case "http":
		return DialHTTP("tcp", addr, opts...)
	default:
		return Dial(protocol, addr, opts...)
	}
}

// 将一次调用RPC请求的信息封装的Call
// 发送给
func (client *Client) send(call *Call) {
	// make sure that the client will send a complete request
	client.sending.Lock()
	defer client.sending.Unlock()
	// log.Printf("send %v", call.Args)
	// register this call
	seq, err := client.registerCall(call)
	if err != nil {
		call.Error = err
		call.done()
		return
	}

	// prepare request header
	client.header.ServiceMethod = call.ServerMethod
	client.header.Seq = seq
	client.header.Error = ""

	// log.Println(client.header)
	// encode and send the request
	if err := client.cc.Write(&client.header, call.Args); err != nil {
		call := client.removeCall(seq)
		// call may be nil, it usually means that write partially failed
		// client has received the response and handled
		if call != nil {
			call.Error = err
			call.done()
		}
	}
}

// Go invokes the function asynchronously
// It returns the Call structure representing the invocation
func (client *Client) Go(serviceMethod string, args, reply interface{}, done chan *Call) *Call {
	if done == nil {
		done = make(chan *Call, 10)
	} else if cap(done) == 0 {
		log.Panic("rpc client: done channel is unbuffered")
	}
	call := &Call{
		ServerMethod: serviceMethod,
		Args:         args,
		Reply:        reply,
		Done:         done,
	}
	client.send(call)
	return call
}

// Call invokes the named function,waits for it to complete
// and returns its error status
func (client *Client) Call(ctx context.Context, serviceMethod string, args, reply interface{}) error {
	call := client.Go(serviceMethod, args, reply, make(chan *Call, 1))
	select {
	case <-ctx.Done():
		client.removeCall(call.Seq)
		return errors.New("rpc client: call failed: " + ctx.Err().Error())
	case call = <-call.Done:
		return call.Error
	}
}
