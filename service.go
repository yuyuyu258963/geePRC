package geerpc

import (
	"go/ast"
	"log"
	"reflect"
	"sync/atomic"
)

// 抽象地对应为一个结构体
type service struct {
	name   string
	typ    reflect.Type
	rcvr   reflect.Value
	method map[string]*methodType
}

func newService(scvr interface{}) *service {
	s := new(service)
	s.rcvr = reflect.ValueOf(scvr)
	s.name = reflect.Indirect(s.rcvr).Type().Name()
	s.typ = reflect.TypeOf(scvr)
	if !ast.IsExported(s.name) {
		log.Fatalf("rpc server: %s is not exported", s.name)
	}
	s.registerMethods()
	return s
}

// 注册结构体上所有可以合法被rpc调用的方法
func (s *service) registerMethods() {
	s.method = make(map[string]*methodType)
	for i := 0; i < s.typ.NumMethod(); i++ {
		method := s.typ.Method(i)
		mType := method.Type
		// 该方法不对外暴露
		if !ast.IsExported(method.Name) {
			continue
		}
		// 只接受两个额外入参，一个返回值
		if mType.NumIn() != 3 || mType.NumOut() != 1 {
			continue
		}
		// 返回值必须是一个error类型
		if mType.Out(0) != reflect.TypeOf((*error)(nil)).Elem() {
			continue
		}
		argvType, replyType := mType.In(1), mType.In(2)
		// 入参必须都是对外暴露的或者是内置的
		if !isExportedOrBuildinType(argvType) || !isExportedOrBuildinType(replyType) {
			continue
		}
		// 检查结束，在map上注册
		s.method[method.Name] = &methodType{
			method:    method,
			ArgType:   argvType,
			ReplyType: replyType,
			numCalls:  0,
		}
		log.Printf("rpc server register: %s.%s", s.name, method.Name)
	}
}

// 实现call方法，根据反射值值调用方法
func (s *service) call(m *methodType, argv, replyv reflect.Value) error {
	atomic.AddUint64(&m.numCalls, 1)
	f := m.method.Func
	returnValues := f.Call([]reflect.Value{s.rcvr, argv, replyv})
	if errInter := returnValues[0].Interface(); errInter != nil {
		return errInter.(error)
	}
	return nil
}

/*
用于抽象某个结构体上的方法
*/
type methodType struct {
	method    reflect.Method // 方法
	ArgType   reflect.Type   // 参数
	ReplyType reflect.Type   // 返回值
	numCalls  uint64         // 记录被调用次数
}

func (m *methodType) NumCalls() uint64 {
	return atomic.LoadUint64(&m.numCalls)
}

// 创建参数的实例
func (m *methodType) newArgv() reflect.Value {
	var argv reflect.Value
	// arg may be pointer type of a value type
	if m.ArgType.Kind() == reflect.Ptr {
		argv = reflect.New(m.ArgType.Elem())
	} else {
		argv = reflect.New(m.ArgType).Elem()
	}

	return argv
}

// 创建返回值实例
func (m *methodType) newReplyv() reflect.Value {
	// reply must to a pointer type
	replyv := reflect.New(m.ReplyType.Elem())
	switch m.ReplyType.Elem().Kind() {
	case reflect.Map:
		replyv.Elem().Set(reflect.MakeMap(m.ReplyType.Elem()))
	case reflect.Slice:
		replyv.Elem().Set(reflect.MakeSlice(m.ReplyType.Elem(), 0, 0))
	}

	return replyv
}

func isExportedOrBuildinType(t reflect.Type) bool {
	return ast.IsExported(t.Name()) || t.PkgPath() == ""
}
