// Package internalhandler is not named handler as doing so interferes with
// godoc links for the api handler package.
package internalhandler

import (
	"context"
	"fmt"
	"sync"

	"github.com/tetratelabs/wazero"
	wazeroapi "github.com/tetratelabs/wazero/api"

	httpwasm "github.com/http-wasm/http-wasm-host-go"
	"github.com/http-wasm/http-wasm-host-go/api"
	"github.com/http-wasm/http-wasm-host-go/api/handler"
	"github.com/http-wasm/http-wasm-host-go/internal"
)

type Runtime struct {
	host                    handler.Host
	runtime                 wazero.Runtime
	hostModule, guestModule wazero.CompiledModule
	newNamespace            httpwasm.NewNamespace
	moduleConfig            wazero.ModuleConfig
	guestConfig             []byte
	logFn                   api.LogFunc
	pool                    sync.Pool
	Features                handler.Features
}

// InitStateKey is a context.Context value associated with a InitState pointer
// to an initializing guest.
type InitStateKey struct{}

type InitState struct {
	Features handler.Features
}

func NewRuntime(ctx context.Context, guest []byte, host handler.Host, options ...httpwasm.Option) (*Runtime, error) {
	o := &internal.WazeroOptions{
		NewRuntime:   internal.DefaultRuntime,
		NewNamespace: internal.DefaultNamespace,
		ModuleConfig: wazero.NewModuleConfig(),
		Logger:       func(context.Context, string) {},
	}
	for _, option := range options {
		option(o)
	}

	wr, err := o.NewRuntime(ctx)
	if err != nil {
		return nil, fmt.Errorf("wasm: error creating runtime: %w", err)
	}

	r := &Runtime{
		host:         host,
		runtime:      wr,
		newNamespace: o.NewNamespace,
		moduleConfig: o.ModuleConfig,
		guestConfig:  o.GuestConfig,
		logFn:        o.Logger,
	}

	if r.hostModule, err = r.compileHost(ctx); err != nil {
		_ = r.Close(ctx)
		return nil, err
	}

	if r.guestModule, err = r.compileGuest(ctx, guest); err != nil {
		_ = r.Close(ctx)
		return nil, err
	}

	// Eagerly add a guest to the pool to catch initialization failure.
	is := &InitState{}
	if g, err := r.newGuest(context.WithValue(ctx, InitStateKey{}, is)); err != nil {
		_ = r.Close(ctx)
		return nil, err
	} else {
		r.pool.Put(g)
	}

	r.Features = is.Features
	return r, nil
}

func (r *Runtime) compileGuest(ctx context.Context, wasm []byte) (wazero.CompiledModule, error) {
	if guest, err := r.runtime.CompileModule(ctx, wasm); err != nil {
		return nil, fmt.Errorf("wasm: error compiling guest: %w", err)
	} else if handle, ok := guest.ExportedFunctions()[handler.FuncHandle]; !ok {
		return nil, fmt.Errorf("wasm: guest doesn't export func[%s]", handler.FuncHandle)
	} else if len(handle.ParamTypes()) != 0 || len(handle.ResultTypes()) != 0 {
		return nil, fmt.Errorf("wasm: guest exports the wrong signature for func[%s]. should be nullary", handler.FuncHandle)
	} else if _, ok = guest.ExportedMemories()[api.Memory]; !ok {
		return nil, fmt.Errorf("wasm: guest doesn't export memory[%s]", api.Memory)
	} else {
		return guest, nil
	}
}

// Handle handles a request by calling guest.handle.
func (r *Runtime) Handle(ctx context.Context) error {
	poolG := r.pool.Get()
	if poolG == nil {
		g, err := r.newGuest(ctx)
		if err != nil {
			return err
		}
		poolG = g
	}
	g := poolG.(*guest)
	defer r.pool.Put(g)
	return g.handle(ctx)
}

// Close implements api.Closer
func (r *Runtime) Close(ctx context.Context) error {
	// We don't have to close any guests as the runtime will close it.
	return r.runtime.Close(ctx)
}

type guest struct {
	ns         wazero.Namespace
	guest      wazeroapi.Module
	handleFunc wazeroapi.Function
}

func (r *Runtime) newGuest(ctx context.Context) (*guest, error) {
	ns, err := r.newNamespace(ctx, r.runtime)
	if err != nil {
		return nil, fmt.Errorf("wasm: error creating namespace: %w", err)
	}

	// Note: host modules don't use configuration
	_, err = ns.InstantiateModule(ctx, r.hostModule, wazero.NewModuleConfig())
	if err != nil {
		_ = ns.Close(ctx)
		return nil, fmt.Errorf("wasm: error instantiating host: %w", err)
	}

	g, err := ns.InstantiateModule(ctx, r.guestModule, r.moduleConfig)
	if err != nil {
		_ = ns.Close(ctx)
		return nil, fmt.Errorf("wasm: error instantiating guest: %w", err)
	}

	return &guest{ns: ns, guest: g, handleFunc: g.ExportedFunction(handler.FuncHandle)}, nil
}

// handle calls the WebAssembly guest function handler.FuncHandle.
func (g *guest) handle(ctx context.Context) (err error) {
	_, err = g.handleFunc.Call(ctx)
	return
}

// enableFeatures implements the WebAssembly host function handler.FuncEnableFeatures.
func (r *Runtime) enableFeatures(ctx context.Context, features uint64) uint64 {
	f := r.host.EnableFeatures(ctx, handler.Features(features))
	return uint64(f)
}

// getConfig implements the WebAssembly host function handler.FuncGetConfig.
func (r *Runtime) getConfig(ctx context.Context, mod wazeroapi.Module,
	buf, bufLimit uint32) (configLen uint32) {
	return writeIfUnderLimit(ctx, mod, buf, bufLimit, r.guestConfig)
}

// log implements the WebAssembly host function handler.FuncLog.
func (r *Runtime) log(ctx context.Context, mod wazeroapi.Module,
	message, messageLen uint32) {
	if messageLen == 0 {
		return // nothing to write
	}
	m := mustReadString(ctx, mod.Memory(), "message", message, messageLen)
	r.logFn(ctx, m)
}

// getURI implements the WebAssembly host function handler.FuncGetURI.
func (r *Runtime) getURI(ctx context.Context, mod wazeroapi.Module,
	buf, bufLimit uint32) (uriLen uint32) {
	path := r.host.GetURI(ctx)
	return writeStringIfUnderLimit(ctx, mod, buf, bufLimit, path)
}

// getRequestHeader implements the WebAssembly host function
// handler.FuncSetURI.
func (r *Runtime) setURI(ctx context.Context, mod wazeroapi.Module,
	uri, uriLen uint32) {
	var p string
	if uriLen > 0 {
		p = mustReadString(ctx, mod.Memory(), "uri", uri, uriLen)
	}
	r.host.SetURI(ctx, p)
}

// getRequestHeader implements the WebAssembly host function
// handler.FuncGetRequestHeader.
func (r *Runtime) getRequestHeader(ctx context.Context, mod wazeroapi.Module,
	name, nameLen, buf, bufLimit uint32) (result uint64) {
	n := mustReadString(ctx, mod.Memory(), "name", name, nameLen)
	v, ok := r.host.GetRequestHeader(ctx, n)
	if !ok {
		return // value doesn't exist
	}
	result = uint64(1<<32) | uint64(writeStringIfUnderLimit(ctx, mod, buf, bufLimit, v))
	return
}

// setResponseHeader implements the WebAssembly host function
// handler.FuncRequestHeader.
func (r *Runtime) setResponseHeader(ctx context.Context, mod wazeroapi.Module,
	name, nameLen, value, valueLen uint32) {
	n := mustReadString(ctx, mod.Memory(), "name", name, nameLen)
	v := mustReadString(ctx, mod.Memory(), "value", value, valueLen)
	r.host.SetResponseHeader(ctx, n, v)
}

// getStatusCode implements the WebAssembly host function
// handler.FuncGetStatusCode.
func (r *Runtime) getStatusCode(ctx context.Context) uint32 {
	return r.host.GetStatusCode(ctx)
}

// setStatusCode implements the WebAssembly host function
// handler.FuncSetStatusCode.
func (r *Runtime) setStatusCode(ctx context.Context, statusCode uint32) {
	r.host.SetStatusCode(ctx, statusCode)
}

// getResponseBody implements the WebAssembly host function
// handler.FuncGetResponseBody.
func (r *Runtime) getResponseBody(ctx context.Context, mod wazeroapi.Module,
	buf, bufLimit uint32) (bodyLen uint32) {
	body := r.host.GetResponseBody(ctx)
	return writeIfUnderLimit(ctx, mod, buf, bufLimit, body)
}

// setResponseBody implements the WebAssembly host function
// handler.FuncSetResponseBody.
func (r *Runtime) setResponseBody(ctx context.Context, mod wazeroapi.Module,
	body, bodyLen uint32) {
	var b []byte
	if bodyLen == 0 {
		b = emptyBody
	} else {
		b = mustRead(ctx, mod.Memory(), "body", body, bodyLen)
	}
	r.host.SetResponseBody(ctx, b)
}

func (r *Runtime) compileHost(ctx context.Context) (wazero.CompiledModule, error) {
	if compiled, err := r.runtime.NewHostModuleBuilder(handler.HostModule).
		ExportFunction(handler.FuncEnableFeatures, r.enableFeatures,
			handler.FuncEnableFeatures, "features").
		ExportFunction(handler.FuncGetConfig, r.getConfig,
			handler.FuncGetConfig, "buf", "buf_limit").
		ExportFunction(handler.FuncLog, r.log,
			handler.FuncLog, "message", "message_len").
		ExportFunction(handler.FuncGetURI, r.getURI,
			handler.FuncGetURI, "buf", "buf_limit").
		ExportFunction(handler.FuncSetURI, r.setURI,
			handler.FuncSetURI, "uri", "uri_len").
		ExportFunction(handler.FuncGetRequestHeader, r.getRequestHeader,
			handler.FuncGetRequestHeader, "name", "name_len", "buf", "buf_limit").
		ExportFunction(handler.FuncSetResponseHeader, r.setResponseHeader,
			handler.FuncSetResponseHeader, "name", "name_len", "value", "value_len").
		ExportFunction(handler.FuncGetStatusCode, r.getStatusCode,
			handler.FuncGetStatusCode).
		ExportFunction(handler.FuncSetStatusCode, r.setStatusCode,
			handler.FuncSetStatusCode, "status_code").
		ExportFunction(handler.FuncGetResponseBody, r.getResponseBody,
			handler.FuncGetResponseBody, "buf", "buf_limit").
		ExportFunction(handler.FuncSetResponseBody, r.setResponseBody,
			handler.FuncSetResponseBody, "body", "body_len").
		ExportFunction(handler.FuncNext, r.host.Next,
			handler.FuncNext).
		Compile(ctx); err != nil {
		return nil, fmt.Errorf("wasm: error compiling host: %w", err)
	} else {
		return compiled, nil
	}
}

// mustReadString is a convenience function that casts mustRead
func mustReadString(ctx context.Context, mem wazeroapi.Memory, fieldName string, offset, byteCount uint32) string {
	if byteCount == 0 {
		return ""
	}
	return string(mustRead(ctx, mem, fieldName, offset, byteCount))
}

var emptyBody = make([]byte, 0)

// mustRead is like api.Memory except that it panics if the offset and byteCount are out of range.
func mustRead(ctx context.Context, mem wazeroapi.Memory, fieldName string, offset, byteCount uint32) []byte {
	if byteCount == 0 {
		return emptyBody
	}
	buf, ok := mem.Read(ctx, offset, byteCount)
	if !ok {
		panic(fmt.Errorf("out of memory reading %s", fieldName))
	}
	return buf
}

func writeIfUnderLimit(ctx context.Context, mod wazeroapi.Module, offset, limit uint32, v []byte) (vLen uint32) {
	vLen = uint32(len(v))
	if vLen > limit {
		return // caller can retry with a larger limit
	} else if vLen == 0 {
		return // nothing to write
	}
	mod.Memory().Write(ctx, offset, v)
	return
}

func writeStringIfUnderLimit(ctx context.Context, mod wazeroapi.Module, offset, limit uint32, v string) (vLen uint32) {
	vLen = uint32(len(v))
	if vLen > limit {
		return // caller can retry with a larger limit
	} else if vLen == 0 {
		return // nothing to write
	}
	mod.Memory().WriteString(ctx, offset, v)
	return
}