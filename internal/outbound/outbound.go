// Package outbound 统一出网连接的代理选择：显式直连或经指定代理，不读取环境变量中的代理。
package outbound

import (
	"errors"
	"net/http"
	"net/url"
	"slices"
)

// proxySchemes 是支持的代理协议。
var proxySchemes = []string{"http", "https", "socks5", "socks5h"}

// ParseProxy 解析代理地址，只接受带主机的 http、https、socks5 或 socks5h URL。
func ParseProxy(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || !slices.Contains(proxySchemes, u.Scheme) {
		return nil, errors.New("代理地址需要有效的 http、https 或 socks5 URL")
	}
	return u, nil
}

// Transport 复制默认传输层并固定出口：proxy 为 nil 时显式直连，否则经该代理连接。
// 返回的传输层由调用方持有，用完后调用 CloseIdleConnections 释放空闲连接。
func Transport(proxy *url.URL) *http.Transport {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	if proxy != nil {
		transport.Proxy = http.ProxyURL(proxy)
	}
	return transport
}
