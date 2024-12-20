package geerpc

import (
	"encoding/json"
	"errors"
	"fmt"
	"geeRPC/codec"
	"io"
	"log"
	"net"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"time"
)

const MagicNumber = 0x3bef5c

type Option struct {
	MagicNumber       int           // Magic number mask this is a geeRpc request
	CodecType         codec.Type    // client may choose different Codec to encode body
	ConnectionTimeout time.Duration //client may choose different ConnectionTimeout
	HandleTimeOut     time.Duration
}

// 默认的选项
var DefaultOption = &Option{
	MagicNumber:       MagicNumber,
	CodecType:         codec.GobType,
	ConnectionTimeout: time.Second * 10,
}

// Server represents an RPC Server
type Server struct {
	serviceMap sync.Map
}

// New Server returns a new Server
func NewServer() *Server {
	return &Server{}
}

// DefaultServer is the default instance of *Server
var DefaultServer = NewServer()

// Register publishes in the server the set of methods
func (server *Server) Register(rcvr interface{}) error {
	s := newService(rcvr)
	if _, dup := server.serviceMap.LoadOrStore(s.name, s); dup {
		return errors.New("rpc: service already defined: " + s.name)
	}
	return nil
}

// Register publishes in the server the set of methods
func Register(rcvr interface{}) error {
	return DefaultServer.Register(rcvr)
}

// findService can use serviceMethod to find the service and method
func (server *Server) findService(serviceMethod string) (sve *service, mtype *methodType, err error) {
	dot := strings.LastIndex(serviceMethod, ".")
	if dot == -1 {
		err = errors.New("rpc server service/method request ill-formed:" + serviceMethod)
		return
	}
	serviceName, methodName := serviceMethod[:dot], serviceMethod[dot+1:]
	svci, ok := server.serviceMap.Load(serviceName)
	if !ok {
		err = errors.New("rpc server service/method : serviceMethod not found")
		return
	}
	sve = svci.(*service)
	mtype = sve.method[methodName]
	if mtype == nil {
		err = errors.New("rpc server: can't find method" + methodName)
	}
	return
}

// Accept accepts connections on the listener and serves requests
func (s *Server) Accept(lis net.Listener) {
	for {
		conn, err := lis.Accept()
		if err != nil {
			log.Println("rpc server: accept error: ", err)
			return
		}
		// log.Println(conn.LocalAddr(), conn.RemoteAddr())
		// 处理connection
		go s.ServerConn(conn)
	}
}

// Accept accepts connections on the listener and serves requests
// for each incoming connection
func Accept(lis net.Listener) {
	DefaultServer.Accept(lis)
}

// ========= 连接建立与协商

func (server *Server) ServerConn(conn io.ReadWriteCloser) {
	defer func() { _ = conn.Close() }()
	var opt Option
	if err := json.NewDecoder(conn).Decode(&opt); err != nil { // server 获取client协商使用的option
		log.Println("rpc server: options error: ", err)
		return
	}
	// log.Printf("rpc server: link option: %+v", opt)
	// verify the connection is RPC's request
	if opt.MagicNumber != MagicNumber {
		log.Printf("rpc: server: invalid magic number %x\n", opt.MagicNumber)
		return
	}
	f := codec.NewCodeFuncMap[opt.CodecType]
	if f == nil {
		log.Printf("rpc server: invalid codec type %s\n", opt.CodecType)
		return
	}
	// log.Printf("opt.HandleTimeout %d\n", opt.HandleTimeOut)
	server.serveCodec(f(conn), opt.HandleTimeOut)
}

// ============ 连接建立后相应通过连接传过来的请求

// invalidRequest is a placeholder for response argv when error occurs
var invalidRequest = struct{}{}

func (server *Server) serveCodec(cc codec.Codec, timeout time.Duration) {
	sending := new(sync.Mutex)
	wg := new(sync.WaitGroup)
	for {
		req, err := server.ReadRequest(cc)
		if err != nil {
			if req == nil {
				break // it's not possible to recover, so close the connection
			}
			req.h.Error = err.Error()
			server.sendResponse(cc, req.h, invalidRequest, sending)
			continue
		}
		// 请求没啥问题，相应请求
		wg.Add(1)
		go server.handleRequest(cc, req, sending, wg, timeout)
	}
	wg.Wait()
	_ = cc.Close()
}

// =========== request的抽象和各个阶段的处理方法

// request stores all information of a call
type request struct {
	h            *codec.Header // header of request
	argv, replyv reflect.Value //argv and replyv of request
	mtype        *methodType
	svc          *service
}

// 读取请求
func (server *Server) ReadRequest(cc codec.Codec) (*request, error) {
	h, err := server.readRequestHeader(cc)
	if err != nil {
		return nil, err
	}

	req := &request{h: h}
	req.svc, req.mtype, err = server.findService(h.ServiceMethod)
	if err != nil {
		return req, err
	}
	req.argv, req.replyv = req.mtype.newArgv(), req.mtype.newReplyv()

	// make sure that argvi is a pointer, ReadBody need a pointer parameter
	argvi := req.argv.Interface()
	if req.argv.Type().Kind() != reflect.Pointer {
		argvi = req.argv.Addr().Interface()
	}
	// * read argv from the request body
	if err = cc.ReadBody(argvi); err != nil {
		log.Println("rpc server: read argv err :", err)
		return req, err
	}
	return req, nil
}

// 读取出头部信息
func (server *Server) readRequestHeader(cc codec.Codec) (*codec.Header, error) {
	var h codec.Header
	if err := cc.ReadHeader(&h); err != nil {
		if err != io.EOF && err != io.ErrUnexpectedEOF {
			log.Println("rpc server: read header error: ", err)
		}
		return nil, err
	}
	// log.Printf("read head:%v\n", h)
	return &h, nil
}

// 回应请求
func (server *Server) sendResponse(cc codec.Codec, h *codec.Header, body interface{}, sending *sync.Mutex) {
	sending.Lock()
	defer sending.Unlock()
	if err := cc.Write(h, body); err != nil {
		log.Println("rpc server: write response error: ", err)
	}
}

// 请求处理
// 考虑两个超时，一个是处理超时，一个是发送response超时
func (server *Server) handleRequest(cc codec.Codec, req *request, sending *sync.Mutex, wg *sync.WaitGroup, timeout time.Duration) {
	defer wg.Done()
	// 分为两个阶段是为了避免重复请求的发送
	called := make(chan struct{})
	send := make(chan struct{})
	// log.Println(req.h, req.argv.Elem())
	go func() {
		// call the rpc method
		err := req.svc.call(req.mtype, req.argv, req.replyv)
		called <- struct{}{}
		if err != nil {
			req.h.Error = err.Error()
			server.sendResponse(cc, req.h, invalidRequest, sending)
			send <- struct{}{}
			return
		}
		server.sendResponse(cc, req.h, req.replyv.Interface(), sending)
		send <- struct{}{}
	}()
	if timeout == 0 {
		<-called
		<-send
		return
	}
	select {
	case <-time.After(timeout):
		req.h.Error = fmt.Sprintf("rpc server : request handle timeout %s", timeout)
		server.sendResponse(cc, req.h, invalidRequest, sending)
	case <-called:
		<-send
	}
}

// =========== 实现支持 HTTP的功能
const (
	connected        = "200 Connected to Gee RPC"
	defaultRPCPath   = "/_geerpc_"
	defaultDebugPath = "/debug/geerpc"
)

// ServeHTTP implements an http.Handler that answers RPC request
// 这部分的逻辑跟前面的有所不同，因为这里是需要接受http请求，通过http的连接后就转到rpc的处理逻辑
func (server *Server) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	// 由于前面套到了HTTP的处理流程中，所以类似的Accept的过程都被代理了啊
	// 而且传入的数据也会自动被解析，所以如果要直接复用这个tcp连接那是不行的，需要先Hijack获得connection的控制权
	// 拒绝非CONNECT的请求
	if req.Method != "CONNECT" {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusMethodNotAllowed)
		_, _ = io.WriteString(w, "405 must CONNECT\n")
	}
	// 自动挟持链接，并使用原始的TCP链接与客户端进行通信
	// 本质上来说就是底层逻辑上的tcp连接传输的信息不会再被自动读取解析了，而是我们可以后续自定义的写入数据
	// 然后自己来读取协商的Option和{header,body}格式传输的rpc信息
	conn, _, err := w.(http.Hijacker).Hijack()
	if err != nil {
		log.Print("rpc hijacking", req.RemoteAddr, ": ", err.Error())
		return
	}
	_, _ = io.WriteString(conn, "HTTP/1.0 "+connected+"\n\n")
	// 之后就将连接的所有权交给rpc服务
	server.ServerConn(conn)
}

// HandleHTTP registers an HTTP request handler for RPC messages on rpcPath
func (server *Server) HandleHTTP() {
	http.Handle(defaultRPCPath, server)
	// 使用同一个server实例，这样就是请求defaultDebugPath的时候可以通过debugHTTP包裹后的ServeHTTP方法来响应显示未
	// Server上注册过的Services的信息
	http.Handle(defaultDebugPath, debugHTTP{server})
	log.Println("rpc server debug path:", defaultDebugPath)
}

// HandleHTTP registers an HTTP request handler for RPC messages on rpcPath
func HandleHTTP() {
	DefaultServer.HandleHTTP()
}
