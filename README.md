[![Build](https://github.com/httpwasm/http-wasm-host-go/workflows/build/badge.svg)](https://github.com/httpwasm/http-wasm-host-go)
[![Go Report Card](https://goreportcard.com/badge/github.com/httpwasm/http-wasm-host-go)](https://goreportcard.com/report/github.com/httpwasm/http-wasm-host-go)
[![License](https://img.shields.io/badge/license-Apache%202.0-blue.svg)](LICENSE)

# http-wasm Host Library for Go

[http-wasm][1] defines HTTP functions implemented in [WebAssembly][2]. This
repository includes [http_handler ABI][3] middleware for various HTTP server
libraries written in Go.

* [nethttp](handler/nethttp): [net/http Handler][4]

# WARNING: This is an early draft

The current maturity phase is early draft. Once this is integrated with
[coraza][5] and [dapr][6], we can begin discussions about compatability.

[1]: https://github.com/http-wasm
[2]: https://webassembly.org/
[3]: https://http-wasm.io/http-handler-abi/
[4]: https://pkg.go.dev/net/http#Handler
[5]: https://github.com/corazawaf/coraza-proxy-wasm
[6]: https://github.com/httpwasm/components-contrib/


# 个人理解
## wasm/host处理流程
- ```handler.Middleware```作为包装器  
```go
type middleware struct {
	host            handler.Host // host实现
	runtime         wazero.Runtime // 运行时
	guestModule     wazero.CompiledModule //编译的模式
	moduleConfig    wazero.ModuleConfig //模块配置
	guestConfig     []byte // wasm二进制文件
	logger          api.Logger
	pool            sync.Pool
	features        handler.Features
	instanceCounter uint64
}
```
- ```handler.Host```接口增加函数   
```go
	// GetTemplate supports the WebAssembly function export FuncGetTemplate.
	GetTemplate(ctx context.Context) string

	// SetTemplate supports the WebAssembly function export FuncSetTemplate.
	SetTemplate(ctx context.Context, template string)
```
- 默认空实现  
```go
func (UnimplementedHost) GetTemplate(context.Context) string { return "" }
func (UnimplementedHost) SetTemplate(context.Context, string) 
```
- 在```handler.middleware.go#692```文件中,声明导入函数,这样在guest/wasm中就能调用了,都是通过指针地址进行数据交换的.
```go 
    //getTemplate
	NewFunctionBuilder().
	WithGoModuleFunction(wazeroapi.GoModuleFunc(m.getTemplate), []wazeroapi.ValueType{i32, i32}, []wazeroapi.ValueType{i32}).
	WithParameterNames("buf", "buf_limit").Export(handler.FuncGetTemplate).
	NewFunctionBuilder().
	//setTemplate
	WithGoModuleFunction(wazeroapi.GoModuleFunc(m.setTemplate), []wazeroapi.ValueType{i32, i32}, []wazeroapi.ValueType{}).
	WithParameterNames("template", "template_len").Export(handler.FuncSetTemplate).
```   


