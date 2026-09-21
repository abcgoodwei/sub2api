package mihomo

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Exercise the shipped kernel's authentication + IN-USER routing, with fake
// loopback upstream proxies. The only public network access is kernel download.
func TestPinnedAccountRoutesWithOfficialKernel(t *testing.T) {
	if os.Getenv("MIHOMO_INSTALL_SMOKE") != "1" {
		t.Skip("opt-in official kernel download")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	m := New(t.TempDir())
	defer m.Close()
	if kernel := os.Getenv("MIHOMO_TEST_BINARY"); kernel != "" {
		b, err := os.ReadFile(kernel)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(m.dir, "mihomo"), b, 0700))
		m.state.Installed = true
	} else {
		require.NoError(t, m.run(ctx, "install", saved{}))
	}
	nodes := []map[string]any{}
	for i, ip := range []string{"203.0.113.1", "203.0.113.1", "203.0.113.2"} {
		proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]string{"ip": ip})
		}))
		defer proxy.Close()
		u, err := url.Parse(proxy.URL)
		require.NoError(t, err)
		port, err := strconv.Atoi(u.Port())
		require.NoError(t, err)
		nodes = append(nodes, map[string]any{"name": "exit-" + strconv.Itoa(i), "type": "http", "server": u.Hostname(), "port": port})
	}
	m.countryLookupURL = "http://example.test/ip"
	require.NoError(t, m.run(ctx, "start", saved{Nodes: nodes}))
	first, firstNode, finish, err := LeasePinnedProbe(ctx, Endpoint, 101)
	require.NoError(t, err)
	finish(true, time.Now().Add(30*time.Minute))
	second, secondNode, finish, err := LeasePinnedProbe(ctx, Endpoint, 102)
	require.NoError(t, err)
	finish(true, time.Now().Add(30*time.Minute))
	require.NotEqual(t, firstNode, secondNode)
	require.Equal(t, nodeBinding(nodes[2]), secondNode)
	request := func(proxy string) string {
		u, err := url.Parse(proxy)
		require.NoError(t, err)
		tr := &http.Transport{Proxy: http.ProxyURL(u), DisableKeepAlives: true}
		defer tr.CloseIdleConnections()
		client := &http.Client{Transport: tr, Timeout: 5 * time.Second}
		resp, err := client.Get("http://example.test/business")
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()
		b, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		return string(b)
	}
	// Round-robin/default group changes cannot affect the two pinned inbounds.
	for range 3 {
		require.Contains(t, request(first), "203.0.113.1")
		require.Contains(t, request(second), "203.0.113.2")
	}
	same, again, finish, err := LeasePinnedProbe(ctx, Endpoint, 101)
	require.NoError(t, err)
	finish(true, time.Now().Add(25*time.Minute))
	require.Equal(t, first, same)
	require.Equal(t, firstNode, again)
	// A config reload preserves the exact authenticated route for active tickets.
	require.NoError(t, m.run(ctx, "once_on", m.saved))
	_, _, finish, err = LeasePinnedProbe(ctx, Endpoint, 101)
	require.NoError(t, err)
	finish(true, time.Now().Add(20*time.Minute))
	require.Equal(t, "used", m.saved.Disabled["exit-0"])
	require.Equal(t, firstNode, m.saved.Bindings["101"].Node)
	require.Contains(t, request(first), "203.0.113.1")
	require.Contains(t, request(second), "203.0.113.2")
	require.NoError(t, m.run(ctx, "disable/exit-0", m.saved))
	_, err = PinnedProxy(101, firstNode)
	require.Error(t, err)
	require.Contains(t, request(second), "203.0.113.2")
}
