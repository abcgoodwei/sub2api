package mihomo

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestPinnedConfigRejectsUnmatchedAndDisabledNodes(t *testing.T) {
	s := saved{Secret: "test", Nodes: []map[string]any{{"name": "first"}, {"name": "second"}}, Disabled: map[string]string{"first": "used", "second": "disabled"}}
	listener, rules := pinnedConfig(s)
	require.Equal(t, "127.0.0.1", listener["listen"])
	require.Len(t, listener["users"], 2)
	require.Len(t, rules, 2)
	require.Contains(t, rules[0], ",first")
	require.Equal(t, "IN-NAME,codex-pinned,REJECT", rules[1])
	s.CountryFilter = CountryFilter{Mode: "include", Codes: []string{"US"}}
	_, rules = pinnedConfig(s)
	require.Equal(t, []string{"IN-NAME,codex-pinned,REJECT"}, rules)
}

func TestPinnedLeasePersistsAccountOwnershipAndExcludesDuplicateIPs(t *testing.T) {
	// Local fake HTTP proxy supplies measured exits; no real network or model.
	nodes := []map[string]any{{"name": "one"}, {"name": "same-ip"}, {"name": "two"}}
	measured := map[string]string{nodeBinding(nodes[0]): "203.0.113.1", nodeBinding(nodes[1]): "203.0.113.1", nodeBinding(nodes[2]): "203.0.113.2"}
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		username, _, ok := func() (string, string, bool) {
			clone := req.Clone(req.Context())
			clone.Header.Set("Authorization", req.Header.Get("Proxy-Authorization"))
			return clone.BasicAuth()
		}()
		if !ok {
			w.WriteHeader(407)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"ip": measured[username]})
	}))
	defer proxy.Close()
	m := New(t.TempDir())
	defer m.Close()
	m.state.Running = true
	m.pinnedProxyURL = proxy.URL
	m.countryLookupURL = "http://exit.test/"
	m.saved = saved{Secret: "test", Nodes: nodes}
	first, binding, finish, err := LeasePinnedProbe(context.Background(), Endpoint, 10)
	require.NoError(t, err)
	waiting, stop := context.WithTimeout(context.Background(), 10*time.Millisecond)
	_, _, _, waitErr := LeasePinnedProbe(waiting, Endpoint, 11)
	stop()
	require.ErrorIs(t, waitErr, context.DeadlineExceeded, "another probe cannot reserve while the first allocation is pending")
	finish(true, time.Now().Add(time.Hour))
	_, secondBinding, finishSecond, err := LeasePinnedProbe(context.Background(), Endpoint, 11)
	require.NoError(t, err)
	require.Equal(t, nodeBinding(nodes[2]), secondBinding, "the second node shares the first account's IP and must be skipped")
	finishSecond(true, time.Now().Add(time.Hour))
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, _, _, err = LeasePinnedProbe(ctx, Endpoint, 12)
	require.ErrorContains(t, err, "no unassigned")
	second, err := PinnedProxy(10, binding)
	require.NoError(t, err)
	require.Equal(t, first, second)
	_, err = PinnedProxy(11, binding)
	require.Error(t, err)
	b, err := os.ReadFile(filepath.Join(m.dir, "settings.json"))
	require.NoError(t, err)
	var stored saved
	require.NoError(t, json.Unmarshal(b, &stored))
	require.Equal(t, "203.0.113.1", stored.Bindings["10"].IP)
	require.Equal(t, binding, stored.Bindings["10"].Node)
	m.saved = stored // Same state after a restart, without selecting a new exit.
	second, err = PinnedProxy(10, binding)
	require.NoError(t, err)
	require.Equal(t, first, second)
	ReleaseAccountBinding(context.Background(), 10, "stale-fingerprint")
	_, err = PinnedProxy(10, binding)
	require.NoError(t, err)
	ReleaseAccountBinding(context.Background(), 10, binding)
	_, err = PinnedProxy(10, binding)
	require.Error(t, err)
}

func TestPinnedProxyRejectsChangedNodeOrCountry(t *testing.T) {
	m := New(t.TempDir())
	defer m.Close()
	node := map[string]any{"name": "one", "server": "original"}
	binding := nodeBinding(node)
	m.saved = saved{Secret: "test", Nodes: []map[string]any{node}, Bindings: map[string]AccountBinding{"1": {Node: binding, IP: "203.0.113.1", ExpiresAt: time.Now().Add(time.Hour)}}}
	m.state.Running = true
	_, err := PinnedProxy(1, binding)
	require.NoError(t, err)
	node["server"] = "replacement"
	_, err = PinnedProxy(1, binding)
	require.Error(t, err)
	node["server"] = "original"
	m.saved.Disabled = map[string]string{"one": "disabled"}
	_, err = PinnedProxy(1, binding)
	require.Error(t, err)
	m.saved.Disabled["one"] = "used"
	_, err = PinnedProxy(1, binding)
	require.NoError(t, err)
	m.saved.CountryFilter = CountryFilter{Mode: "include", Codes: []string{"US"}}
	_, err = PinnedProxy(1, binding)
	require.Error(t, err)
}

func TestPinnedSecretsStayOutOfStatus(t *testing.T) {
	m := New(t.TempDir())
	defer m.Close()
	n := map[string]any{"name": "node", "password": "upstream-secret"}
	m.saved = saved{Secret: "controller-secret", Nodes: []map[string]any{n}, Bindings: map[string]AccountBinding{"42": {Node: nodeBinding(n), IP: "203.0.113.42", ExpiresAt: time.Now().Add(time.Hour)}}}
	b, err := json.Marshal(m.Status())
	require.NoError(t, err)
	require.Contains(t, string(b), `"bound_account_id":42`)
	require.Contains(t, string(b), "203.0.113.42")
	require.False(t, strings.Contains(string(b), "secret"))
}
