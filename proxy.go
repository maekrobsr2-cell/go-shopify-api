package main

import (
	"bufio"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
)

// ─── Lock-free proxy rotator ─────────────────────────────────────────────────

type ProxyRotator struct {
	proxies []string
	index   atomic.Uint64
	mu      sync.RWMutex
}

func NewProxyRotator(proxies []string) *ProxyRotator {
	return &ProxyRotator{proxies: proxies}
}

func (pr *ProxyRotator) Next() string {
	pr.mu.RLock()
	defer pr.mu.RUnlock()
	if len(pr.proxies) == 0 {
		return ""
	}
	idx := pr.index.Add(1) - 1
	return pr.proxies[idx%uint64(len(pr.proxies))]
}

func (pr *ProxyRotator) Len() int {
	pr.mu.RLock()
	defer pr.mu.RUnlock()
	return len(pr.proxies)
}

func (pr *ProxyRotator) Remove(proxyURL string) {
	normalized := normalizeProxy(proxyURL)
	pr.mu.Lock()
	defer pr.mu.Unlock()
	filtered := make([]string, 0, len(pr.proxies))
	for _, p := range pr.proxies {
		if normalizeProxy(p) != normalized {
			filtered = append(filtered, p)
		}
	}
	pr.proxies = filtered
}

// ─── Proxy normalization ─────────────────────────────────────────────────────

func normalizeProxy(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	// Add scheme if missing
	if !strings.Contains(p, "://") {
		// Detect socks
		if strings.HasPrefix(strings.ToLower(p), "socks") {
			p = "socks5://" + p
		} else {
			p = "http://" + p
		}
	}

	// Handle user:pass@host:port format without scheme
	parsed, err := url.Parse(p)
	if err != nil {
		return p
	}
	return parsed.String()
}

// ─── Load proxies from file ──────────────────────────────────────────────────

func loadProxies(filename string) []string {
	if filename == "" {
		return nil
	}
	f, err := os.Open(filename)
	if err != nil {
		return nil
	}
	defer f.Close()

	var proxies []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		normalized := normalizeProxy(line)
		if normalized != "" {
			proxies = append(proxies, normalized)
		}
	}
	return proxies
}

// ─── Find proxy file ─────────────────────────────────────────────────────────

func findProxyFile() string {
	candidates := []string{"working_proxies.txt", "px.txt"}
	for _, name := range candidates {
		if _, err := os.Stat(name); err == nil {
			fmt.Printf("[PROXY] Found proxy file: %s\n", name)
			return name
		}
	}
	return ""
}
