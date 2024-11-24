package xclient

import (
	"errors"
	"math"
	"math/rand"
	"sync"
	"time"
)

type SelectMode int

const (
	RandomSelect     SelectMode = iota // select randomly
	RoundRobinSelect                   // select using Round Robin
)

// 服务发现需要实现的基本接口
type Discovery interface {
	Refresh() error                      // 从注册中心更新服务列表
	Update(servers []string) error       // 手动更新服务列表
	Get(mode SelectMode) (string, error) // 根据负载均衡策略，选择一个服务实例
	GetAll() ([]string, error)           // 返回所有的服务实例
}

// MultiServerDiscovery is a discovery for multi servers with a register center
// user provides the server address explicitly instead
type MultiServerDiscovery struct {
	r       *rand.Rand   // generate random number
	mu      sync.RWMutex // protect following
	servers []string
	index   int // record the selected position for robin algorithm
}

var _ Discovery = (*MultiServerDiscovery)(nil)

// NewMultiServerDiscovery creates a new MultiServerDiscovery instance
func NewMultiServerDiscovery(servers []string) *MultiServerDiscovery {
	d := &MultiServerDiscovery{
		servers: servers,
		r:       rand.New(rand.NewSource(time.Now().UnixNano())),
	}
	d.index = d.r.Intn(math.MaxInt32 - 1)
	return d
}

// 这个暂时没啥用，因为没有注册中心给我们刷新
func (d *MultiServerDiscovery) Refresh() error {
	return nil
}

// Update update the servers of discovery dynamically if needed
func (d *MultiServerDiscovery) Update(servers []string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.servers = servers
	return nil
}

// Get a server according to mode
// error is needed for mode maybe unknown or there is no servers in register
func (d *MultiServerDiscovery) Get(mode SelectMode) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	n := len(d.servers)
	if n == 0 {
		return "", errors.New("rpc discovery: no available servers")
	}
	switch mode {
	case RandomSelect:
		// return d.servers[0], nil
		return d.servers[d.r.Intn(n)], nil
	case RoundRobinSelect:
		s := d.servers[d.index%n] // 每次都求模式必要的，因为servers可能在动态变化
		d.index = (d.index + 1) % n
		return s, nil
	default:
		return "", errors.New("rpc discovery: not support select mode: ")
	}
}

// returns all server in discovery
func (d *MultiServerDiscovery) GetAll() ([]string, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	// return a copy of d.servers
	// 这个copy就比较细节了，这是因为切片底层共享一个数组，如果用户难道了，不能让它随意更改
	// 虽然这个服务发现和请求的操作通常通过网络实现
	servers := make([]string, len(d.servers))
	copy(servers, d.servers)
	return servers, nil
}
