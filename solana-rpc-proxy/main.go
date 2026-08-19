// solana-rpc-proxy provides the wallet container a narrow path to Solana when
// the server Docker bridge cannot reach the public RPC directly. It binds to
// the host LAN address, accepts only the bridge subnet, and proxies POST JSON-RPC
// requests to one fixed URL.
package main

import (
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/netip"
	"net/url"
	"os"
	"time"
)

const (
	defaultListenAddress = "192.168.1.5:8899"
	defaultAllowedSubnet = "172.24.0.0/16"
	defaultUpstreamURL   = "https://api.devnet.solana.com"
)

func main() {
	upstream, err := url.Parse(envOr("SOLANA_RPC_UPSTREAM", defaultUpstreamURL))
	if err != nil {
		log.Fatalf("invalid SOLANA_RPC_UPSTREAM: %v", err)
	}

	allowedSubnet, err := netip.ParsePrefix(envOr("RPC_PROXY_ALLOWED_SUBNET", defaultAllowedSubnet))
	if err != nil {
		log.Fatalf("invalid RPC_PROXY_ALLOWED_SUBNET: %v", err)
	}

	proxy := httputil.NewSingleHostReverseProxy(upstream)
	originalDirector := proxy.Director
	proxy.Director = func(request *http.Request) {
		originalDirector(request)
		request.Host = upstream.Host
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		log.Printf("solana rpc request failed: %v", err)
		http.Error(w, "solana rpc unavailable", http.StatusBadGateway)
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !allowedClient(r, allowedSubnet) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if r.URL.Path == "/health" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		proxy.ServeHTTP(w, r)
	})

	server := &http.Server{
		Addr:              envOr("RPC_PROXY_LISTEN_ADDR", defaultListenAddress),
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}
	log.Printf("Solana RPC proxy listening on %s", server.Addr)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}

func allowedClient(request *http.Request, subnet netip.Prefix) bool {
	host, _, err := net.SplitHostPort(request.RemoteAddr)
	if err != nil {
		return false
	}
	address, err := netip.ParseAddr(host)
	return err == nil && subnet.Contains(address)
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
