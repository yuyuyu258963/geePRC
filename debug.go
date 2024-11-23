package geerpc

import (
	"fmt"
	"html/template"
	"net/http"
)

const debugText = `
	<html>
	<body>
	<title>GeeRPC Services</title>
	{{range .}}
	<hr>
	Service {{.Name}}
	<hr>

		<table>
		<th align=center>Method</th><th align=center>Calls</th>
		{{range $name, $mtype := .Method}}
			<tr>
			<td align=left font=fixed>{{$name}}({{$mtype.ArgType}}, {{$mtype.ReplyType}}) error</td>
			<td align=center>{{$mtype.NumCalls}}</td>
			</tr>
		{{end}}
		</table>
	{{end}}
	</body>
	</html>
`

// 创建一个用于DEBUG的网页模版
var debug = template.Must(template.New("RPC debug").Parse(debugText))

// debugHTTP 是对Server的套壳
type debugHTTP struct {
	*Server
}

type debugService struct {
	Name   string
	Method map[string]*methodType
}

// Runs at /debug/geerpc
func (server debugHTTP) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Build a sorted version of data.
	var services []debugService
	server.serviceMap.Range(func(namei, svci interface{}) bool {
		svc := svci.(*service)
		services = append(services, debugService{
			Name:   namei.(string),
			Method: svc.method,
		})
		return true
	})
	// 也就是说一旦来了一个请求就将Template作为结果返回，并且将处理过的services作为数据传入
	err := debug.Execute(w, services)
	if err != nil {
		_, _ = fmt.Fprintf(w, "rpc: error executing template: %v", err)
	}
}
