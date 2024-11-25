package registry

import (
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

// GeeRegistry is a simple register center, provide following functions
// add a server and receive heartbeat to keep it alive
// returns all alive servers and delete dead servers sync simultaneously
type GeeRegistry struct {
	timeout time.Duration
	mu      sync.Mutex
	servers map[string]*ServerItem
}

// 抽象为单个服务器实例
type ServerItem struct {
	Addr  string
	start time.Time
}

const (
	// 默认的注册中心的地址
	defaultPath = "/_geerpc/_registry"
	// 默认最长断连时间
	defaultTimeout = time.Minute * 5
)

// 默认的注册中心
var DefaultGeeRegistry *GeeRegistry

func init() {
	DefaultGeeRegistry = New(defaultTimeout)
}

// New create a registry instance with timeout setting
func New(timeout time.Duration) *GeeRegistry {
	return &GeeRegistry{
		servers: make(map[string]*ServerItem),
		timeout: timeout,
	}
}

// 新注册一个服务器
func (r *GeeRegistry) putServer(addr string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.servers[addr]
	if s == nil {
		r.servers[addr] = &ServerItem{Addr: addr, start: time.Now()}
	} else {
		r.servers[addr].start = time.Now()
	}
}

// 返回依然存活的服务器列表
func (r *GeeRegistry) aliveServers() []string {
	r.mu.Lock()
	r.mu.Unlock()
	var alive []string
	for addr, s := range r.servers {
		if r.timeout == 0 || s.start.Add(r.timeout).After(time.Now()) {
			alive = append(alive, addr)
		} else {
			delete(r.servers, addr)
		}
	}
	return alive
}

// Run at /_geerpc_/registry
func (r *GeeRegistry) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	switch req.Method {
	case "GET": // 只要是get方法就直接返回
		// keep it simple, server is in req.Header
		w.Header().Set("X-Geerpc-Servers", strings.Join(r.aliveServers(), ","))
	case "POST":
		addr := req.Header.Get("X-Geerpc-Server")
		if addr == "" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		r.putServer(addr)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// HandleHTTP registers an HTTP request handler for GeeRegistry messages on registryPath
func (r *GeeRegistry) HandleHTTP(registryPath string) {
	http.Handle(registryPath, r)
	log.Println("rpc registry path:", registryPath)
}

// 对外暴露默认实例的接口
func HandleHTTP() {
	DefaultGeeRegistry.HandleHTTP(defaultPath)
}

// heartbeat send a heartbeat message every once in a while
// it's a helper function for server to register or send heartbeat
// 为服务端提供心跳检测的函数，来定期向注册中心注册自己
// 在Server实例初始化的时候调用，后续能定期发送心跳信息
func Heartbeat(registry, addr string, duration time.Duration) {
	if duration == 0 {
		// make sure there is enough time to send heat beat
		duration = defaultTimeout - time.Duration(1)*time.Minute
	}
	var err error
	err = sendHeartbeat(registry, addr)
	go func() {
		t := time.NewTicker(duration)
		defer t.Stop()
		for err == nil {
			<-t.C
			err = sendHeartbeat(registry, addr)
		}
	}()
}

func sendHeartbeat(registry, addr string) error {
	log.Println(addr, "send heat beat to registry", registry)
	httpClient := &http.Client{}
	req, _ := http.NewRequest("POST", registry, nil)
	req.Header.Set("X-Geerpc-Server", addr)
	if _, err := httpClient.Do(req); err != nil {
		log.Println("rpc server: heart beat err:", err)
		return err
	}
	return nil
}
