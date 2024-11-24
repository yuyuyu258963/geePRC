package xclient

import (
	"context"
	. "geeRPC"
	"io"
	"reflect"
	"sync"
)

type XClient struct {
	d       Discovery // 服务发现
	mode    SelectMode
	opt     *Option
	mu      sync.Mutex // guards
	clients map[string]*Client
}

var _ io.Closer = (*XClient)(nil)

func NewXClient(d Discovery, mode SelectMode, opt *Option) *XClient {
	return &XClient{d: d, mode: mode, opt: opt, clients: make(map[string]*Client)}
}

func (xc *XClient) Close() error {
	xc.mu.Lock()
	defer xc.mu.Unlock()
	for key, client := range xc.clients {
		// I have no idea how to deal with error, just ignore it
		_ = client.Close()
		delete(xc.clients, key)
	}
	return nil
}

func (xc *XClient) dial(rpcAddr string) (*Client, error) {
	xc.mu.Lock()
	defer xc.mu.Unlock()
	// 先尝试拿之前已经存在的client来发送（其实是复用底层的连接）
	client, ok := xc.clients[rpcAddr]
	if ok && !client.IsAvailable() {
		_ = client.Close()
		delete(xc.clients, rpcAddr)
		client = nil
	}
	// 如果之前没有对这个rpcAddr进行rpc调用过则新增一个Client来进行rpc调用
	if client == nil {
		var err error
		// 尝试新增一个Client
		client, err = XDial(rpcAddr, xc.opt)
		if err != nil {
			return nil, err
		}
		xc.clients[rpcAddr] = client
	}
	return client, nil
}

// 封装使用
func (xc *XClient) call(rpcAddr string, ctx context.Context, serviceMethod string, args, reply interface{}) error {
	client, err := xc.dial(rpcAddr) // 创建一个client来对应请求rpcAddr进行rpc调用
	if err != nil {
		return err
	}
	// fmt.Printf("args %v\n", args)
	return client.Call(ctx, serviceMethod, args, reply)
}

// 是不是对Client.Call实现了再封装
// XClient可以自动选择一个rpcAddr来执行请求的调用的serviceMethod指定的rpc
// 现在其实已经要思维转换了，能够执行rpc的服务器并不是一台了，而是可能有好多
// 而且这个Server集群可能是动态变化的，所以不需要用户指定要（或不知道）执行rpc的服务器的address
// 而是这里直接通过负载均衡算法找到一个可用的rpc服务器，然后让这个rpc服务器执行remote call
func (xc *XClient) Call(ctx context.Context, serviceMethod string, args, reply interface{}) error {
	rpcAddr, err := xc.d.Get(xc.mode) // 选择一个服务器
	if err != nil {
		return err
	}
	return xc.call(rpcAddr, ctx, serviceMethod, args, reply)
}

// Broadcast invokes the named function for every server registered int discovery center
func (xc *XClient) Broadcast(ctx context.Context, serviceMethod string, args, reply interface{}) error {
	servers, err := xc.d.GetAll() // 尝试获取所有的已注册服务器
	if err != nil {
		return err
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	var e error
	replyDone := reply == nil              // if reply is nil, don't need to set value
	ctx, cancel := context.WithCancel(ctx) // 实现一个成功了，则取消所有其他的的关键
	defer cancel()                         // 尽量退出的时候都cancel一下
	for _, rpcAddr := range servers {
		wg.Add(1)
		go func(rpcAddr string) {
			defer wg.Done()
			var clonedReply interface{}
			if reply != nil {
				// 这里其实是为了后面并发的安全，因为reply应该是一个指针
				// 后面传入的话会导致并发进程使用一个指针，这很危险，所以要创建多个指向不同内存的
				// 来接受各个请求可能传回来的结果
				clonedReply = reflect.New(reflect.ValueOf(reply).Elem().Type()).Interface()
			}
			err = xc.call(rpcAddr, ctx, serviceMethod, args, clonedReply)
			mu.Lock()
			if err != nil && e == nil {
				e = err
				cancel() // if any call failed, cancel unfinished calls
			}
			// 如果此次rpc调用成功，且是需要保存返回值的，则要将返回值赋值回原指针指向的地址
			if err == nil && !replyDone {
				reflect.ValueOf(reply).Elem().Set(reflect.ValueOf(clonedReply).Elem())
				replyDone = true // 标记已经接收过值了
			}
			mu.Unlock()
		}(rpcAddr)
	}
	wg.Wait()
	return e
}
