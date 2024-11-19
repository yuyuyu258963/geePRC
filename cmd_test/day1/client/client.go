package main

import (
	"encoding/json"
	"fmt"
	geerpc "geeRPC"
	"geeRPC/codec"
	"log"
	"net"
	"time"
)

func main() {
	log.SetFlags(log.Llongfile | log.LstdFlags)

	// in fact, following code is like a simple geerpc client
	conn, err := net.Dial("tcp", "localhost:9999")
	if err != nil {
		fmt.Println("error: ", err)
	}
	defer func() { _ = conn.Close() }()

	fmt.Println("start")
	time.Sleep(time.Second)
	// send options
	_ = json.NewEncoder(conn).Encode(geerpc.DefaultOption)
	cc := codec.NewGobCodec(conn)
	// send request & receive response
	for i := 0; i < 5; i++ {
		h := &codec.Header{
			ServiceMethod: "Foo.Sum",
			Seq:           uint64(i),
		}
		_ = cc.Write(h, fmt.Sprintf("geerpc req %d", h.Seq))
		_ = cc.ReadHeader(h)
		// var reply string
		// _ = cc.ReadBody(&reply)
		// log.Println("reply:", reply)
	}

	select {}
}
