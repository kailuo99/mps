package mps

import (
	"context"
	"github.com/telanflow/mps/pool"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"regexp"
)

var (
	HttpTunnelOk   = []byte("HTTP/1.0 200 OK\r\n\r\n")
	HttpTunnelFail = []byte("HTTP/1.1 502 Bad Gateway\r\n\r\n")
	hasPort        = regexp.MustCompile(`:\d+$`)
)

// The tunnel proxy type. Implements http.Handler.
type TunnelHandler struct {
	Ctx           *Context
	BufferPool    httputil.BufferPool
	ConnContainer pool.ConnContainer
}

// Create a tunnel handler
func NewTunnelHandler() *TunnelHandler {
	return &TunnelHandler{
		Ctx:           NewContext(),
		BufferPool:    pool.DefaultBuffer,
		ConnContainer: pool.NewConnProvider(pool.DefaultConnOptions),
	}
}

// Create a tunnel handler with Context
func NewTunnelHandlerWithContext(ctx *Context) *TunnelHandler {
	return &TunnelHandler{
		Ctx:           ctx,
		BufferPool:    pool.DefaultBuffer,
		ConnContainer: pool.NewConnProvider(pool.DefaultConnOptions),
	}
}

// Standard net/http function. You can use it alone
func (tunnel *TunnelHandler) ServeHTTP(rw http.ResponseWriter, req *http.Request) {
	// hijacker connection
	proxyClient, err := hijacker(rw)
	if err != nil {
		http.Error(rw, err.Error(), 502)
		return
	}

	var (
		u              *url.URL = nil
		targetConn     net.Conn = nil
		targetAddr              = hostAndPort(req.URL.Host)
		isCascadeProxy          = false
	)
	if tunnel.Ctx.Transport != nil && tunnel.Ctx.Transport.Proxy != nil {
		u, err = tunnel.Ctx.Transport.Proxy(req)
		if err != nil {
			ConnError(proxyClient)
			return
		}
		if u != nil {
			// connect addr eg. "localhost:80"
			targetAddr = hostAndPort(u.Host)
			isCascadeProxy = true
		}
	}

	// connect to targetAddr
	if tunnel.ConnContainer != nil {
		targetConn, err = tunnel.ConnContainer.Get(targetAddr)
		if err != nil {
			targetConn, err = tunnel.ConnectDial("tcp", targetAddr)
		}
	} else {
		targetConn, err = tunnel.ConnectDial("tcp", targetAddr)
	}
	if err != nil {
		ConnError(proxyClient)
		return
	}

	// If the ConnContainer is exists,
	// When io.CopyBuffer is complete,
	// put the idle connection into the ConnContainer so can reuse it next time
	if tunnel.ConnContainer != nil {
		defer tunnel.ConnContainer.Put(targetConn)
	} else {
		defer targetConn.Close()
	}

	// The cascade proxy needs to forward the request
	if isCascadeProxy {
		// The cascade proxy needs to send it as-is
		_ = req.Write(targetConn)
	} else {
		// Tell client that the tunnel is ready
		_, _ = proxyClient.Write(HttpTunnelOk)
	}

	go func() {
		buf := tunnel.buffer().Get()
		_, _ = io.CopyBuffer(targetConn, proxyClient, buf)
		tunnel.buffer().Put(buf)
		_ = proxyClient.Close()
	}()
	buf := tunnel.buffer().Get()
	_, _ = io.CopyBuffer(proxyClient, targetConn, buf)
	tunnel.buffer().Put(buf)
}

func (tunnel *TunnelHandler) ConnectDial(network, addr string) (net.Conn, error) {
	if tunnel.Ctx.Transport != nil && tunnel.Ctx.Transport.DialContext != nil {
		return tunnel.Ctx.Transport.DialContext(tunnel.Context(), network, addr)
	}
	return net.Dial(network, addr)
}

func (tunnel *TunnelHandler) Context() context.Context {
	if tunnel.Ctx.Context != nil {
		return tunnel.Ctx.Context
	}
	return context.Background()
}

// Transport
func (tunnel *TunnelHandler) Transport() *http.Transport {
	return tunnel.Ctx.Transport
}

// Get buffer pool
func (tunnel *TunnelHandler) buffer() httputil.BufferPool {
	if tunnel.BufferPool != nil {
		return tunnel.BufferPool
	}
	return pool.DefaultBuffer
}

func hostAndPort(addr string) string {
	if !hasPort.MatchString(addr) {
		addr += ":80"
	}
	return addr
}

func ConnError(w net.Conn) {
	_, _ = w.Write(HttpTunnelFail)
	_ = w.Close()
}
