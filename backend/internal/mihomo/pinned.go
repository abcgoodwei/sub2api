package mihomo

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

const pinnedListenerName = "codex-pinned"
const pinnedEndpoint = "http://127.0.0.1:3102"

// The fingerprint covers the full outbound configuration, not its display name.
// A subscription replacing a node must never silently move a live ticket.
func nodeBinding(node map[string]any) string {
	b, _ := json.Marshal(node)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
func pinPassword(secret, binding string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte("codex-egress:" + binding))
	return hex.EncodeToString(mac.Sum(nil))
}
func pinURL(secret, binding string) string {
	u, _ := url.Parse(pinnedEndpoint)
	u.User = url.UserPassword(binding, pinPassword(secret, binding))
	return u.String()
}
func pinnedNodeAllowed(s saved, name string) bool {
	return (s.Disabled[name] == "" || s.Disabled[name] == "used") && countryAllowed(s, name)
}
func pinnedConfig(s saved) (map[string]any, []string) {
	// An empty users array disables authentication. Keep a deny-only credential
	// even when no node is eligible, and reject all unmatched pinned traffic.
	users := []map[string]string{{"username": "unavailable", "password": pinPassword(s.Secret, "unavailable")}}
	rules := []string{}
	for _, node := range s.Nodes {
		name, _ := node["name"].(string)
		if !pinnedNodeAllowed(s, name) {
			continue
		}
		binding := nodeBinding(node)
		users = append(users, map[string]string{"username": binding, "password": pinPassword(s.Secret, binding)})
		rules = append(rules, "AND,((IN-NAME,"+pinnedListenerName+"),(IN-USER,"+binding+")),"+name)
	}
	rules = append(rules, "IN-NAME,"+pinnedListenerName+",REJECT")
	return map[string]any{"name": pinnedListenerName, "type": "mixed", "listen": "127.0.0.1", "port": 3102, "udp": false, "users": users}, rules
}

// PinnedProxy returns a credentialed local route only while the exact node is
// present and permitted. Credentials never need to be persisted with tickets.
func PinnedProxy(accountID int64, binding string) (string, error) {
	var proxy string
	managers.Range(func(key, _ any) bool {
		m, ok := key.(*Manager)
		if !ok {
			return true
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		lease := m.saved.Bindings[strconv.FormatInt(accountID, 10)]
		if m.closed || !m.state.Running || lease.Node != binding || !time.Now().Before(lease.ExpiresAt) {
			return true
		}
		for _, node := range m.saved.Nodes {
			name, _ := node["name"].(string)
			if lease.Name != "" && name != lease.Name {
				continue
			}
			if nodeBinding(node) == binding && pinnedNodeAllowed(m.saved, name) {
				proxy = m.pinURL(m.saved.Secret, binding)
				return false
			}
		}
		return true
	})
	if proxy == "" {
		return "", errors.New("ticket egress node unavailable")
	}
	return proxy, nil
}

// AccountBinding is the durable exclusive exit allocation for one account.
type AccountBinding struct {
	Name      string    `json:"name,omitempty"`
	Node      string    `json:"node"`
	IP        string    `json:"ip"`
	ExpiresAt time.Time `json:"expires_at"`
}

// ReleaseAccountBinding invalidates every model on the rejected account exit.
// The fingerprint guard prevents delayed feedback retiring a replacement exit.
func ReleaseAccountBinding(ctx context.Context, accountID int64, binding string) {
	managers.Range(func(key, _ any) bool {
		m, ok := key.(*Manager)
		if !ok {
			return true
		}
		if m.acquire(ctx) != nil {
			return true
		}
		defer m.release()
		m.mu.Lock()
		defer m.mu.Unlock()
		id := strconv.FormatInt(accountID, 10)
		if m.saved.Bindings[id].Node != binding {
			return true
		}
		next := cloneBindings(m.saved.Bindings)
		delete(next, id)
		snapshot := m.saved
		snapshot.Bindings = next
		b, _ := json.Marshal(snapshot)
		if atomicWrite(filepath.Join(m.dir, "settings.json"), b, 0600) == nil {
			m.saved = snapshot
		}
		return true
	})
}
func cloneBindings(old map[string]AccountBinding) map[string]AccountBinding {
	next := make(map[string]AccountBinding, len(old))
	for k, v := range old {
		next[k] = v
	}
	return next
}
func (m *Manager) measurePinnedIP(ctx context.Context, proxy string) (string, error) {
	u, _ := url.Parse(proxy)
	tr := &http.Transport{Proxy: http.ProxyURL(u), DisableKeepAlives: true, TLSHandshakeTimeout: 3 * time.Second}
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr, Timeout: 6 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	endpoint := m.countryLookupURL
	if endpoint == "" {
		endpoint = "https://api.country.is/"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", errors.New("cannot measure ticket egress IP")
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", errors.New("cannot measure ticket egress IP")
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 8193))
	var result struct {
		IP string `json:"ip"`
	}
	if err != nil || resp.StatusCode != 200 || len(b) > 8192 || json.Unmarshal(b, &result) != nil || net.ParseIP(result.IP) == nil {
		return "", errors.New("cannot measure ticket egress IP")
	}
	return net.ParseIP(result.IP).String(), nil
}

// LeasePinnedProbe chooses a concrete node BEFORE sending the probe. Both the
// probe and subsequent business connections use the same authenticated route;
// neither traverses CODEX-ROTATE. A lease only serializes probes, not business.
// Non-managed proxies retain their existing behavior and have no node binding.
func LeasePinnedProbe(ctx context.Context, proxy string, accountID int64) (egress, binding string, release func(bool, time.Time), err error) {
	noop := func(bool, time.Time) {}
	if proxy != Endpoint {
		return proxy, "", noop, nil
	}
	var m *Manager
	managers.Range(func(key, _ any) bool {
		candidate, ok := key.(*Manager)
		if !ok {
			return true
		}
		candidate.mu.Lock()
		ready := !candidate.closed && candidate.state.Running
		candidate.mu.Unlock()
		if ready {
			m = candidate
			return false
		}
		return true
	})
	if m == nil || accountID <= 0 {
		return "", "", nil, errors.New("managed ticket egress is not ready")
	}
	if err = m.acquire(ctx); err != nil {
		return "", "", nil, err
	}
	m.mu.Lock()
	snapshot := m.saved
	ready := !m.closed && m.state.Running
	start := m.probeCursor
	m.mu.Unlock()
	if !ready {
		m.release()
		return "", "", nil, errors.New("managed ticket egress is not ready")
	}
	id := strconv.FormatInt(accountID, 10)
	old := snapshot.Bindings[id]
	bindings := cloneBindings(snapshot.Bindings)
	now := time.Now()
	for key, value := range bindings {
		if !now.Before(value.ExpiresAt) {
			delete(bindings, key)
		}
	}
	name, ip := "", ""
	reused := false
	for _, node := range snapshot.Nodes {
		n, _ := node["name"].(string)
		if old.Node == nodeBinding(node) && now.Before(old.ExpiresAt) && pinnedNodeAllowed(snapshot, n) {
			name, binding, ip, reused = n, old.Node, old.IP, true
			break
		}
	}
	if !reused {
		delete(bindings, id)
		// Bound the network-only allocation pass; later rounds resume the cursor.
		lookupCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		for i := 0; i < len(snapshot.Nodes) && lookupCtx.Err() == nil; i++ {
			index := (int(start%uint64(len(snapshot.Nodes))) + i) % len(snapshot.Nodes)
			node := snapshot.Nodes[index]
			n, _ := node["name"].(string)
			m.mu.Lock()
			m.probeCursor = uint64(index + 1)
			m.mu.Unlock()
			if n == "" || snapshot.Disabled[n] != "" || !countryAllowed(snapshot, n) {
				continue
			}
			candidate := nodeBinding(node)
			occupied := false
			for _, owner := range bindings {
				if owner.Node == candidate {
					occupied = true
					break
				}
			}
			if occupied {
				continue
			}
			measured, measureErr := m.measurePinnedIP(lookupCtx, m.pinURL(snapshot.Secret, candidate))
			if measureErr != nil {
				continue
			}
			for _, owner := range bindings {
				if owner.IP == measured {
					occupied = true
					break
				}
			}
			if occupied {
				continue
			}
			name, binding, ip = n, candidate, measured
			break
		}
	}
	if name == "" {
		m.release()
		return "", "", nil, errors.New("no unassigned ticket egress IP available")
	}
	// Reserve before probing so crashes cannot assign this IP to another account.
	// A bounded provisional lease is replaced with the actual ticket expiry.
	expires := now.Add(2 * time.Minute)
	if reused && old.ExpiresAt.After(expires) {
		expires = old.ExpiresAt
	}
	bindings[id] = AccountBinding{Name: name, Node: binding, IP: ip, ExpiresAt: expires}
	snapshot.Bindings = bindings
	if snapshot.UseOnce {
		retired := make(map[string]string, len(snapshot.Disabled)+1)
		for k, v := range snapshot.Disabled {
			retired[k] = v
		}
		retired[name] = "used"
		snapshot.Disabled = retired
	}
	stored, _ := json.Marshal(snapshot)
	if err = atomicWrite(filepath.Join(m.dir, "settings.json"), stored, 0600); err != nil {
		m.release()
		return "", "", nil, errors.New("cannot reserve ticket egress IP")
	}
	m.mu.Lock()
	m.saved = snapshot
	m.mu.Unlock()
	var once sync.Once
	release = func(success bool, expiresAt time.Time) {
		once.Do(func() {
			defer m.release()
			next := snapshot
			next.Bindings = cloneBindings(snapshot.Bindings)
			if success {
				if reused && old.ExpiresAt.After(expiresAt) {
					expiresAt = old.ExpiresAt
				}
				next.Bindings[id] = AccountBinding{Name: name, Node: binding, IP: ip, ExpiresAt: expiresAt}
			} else if reused {
				next.Bindings[id] = old
			} else {
				delete(next.Bindings, id)
			}
			b, _ := json.Marshal(next)
			if atomicWrite(filepath.Join(m.dir, "settings.json"), b, 0600) != nil {
				m.stop()
				return
			}
			m.mu.Lock()
			m.saved = next
			m.mu.Unlock()
			if !snapshot.UseOnce {
				return
			}
			finishCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			action := "used/" + name
			if !success && !reused {
				action = "failed/" + name
			}
			if m.run(finishCtx, action, next) != nil {
				m.stop()
			}
		})
	}
	return m.pinURL(snapshot.Secret, binding), binding, release, nil
}

// PinnedNodeName exposes only the non-secret internal node name for diagnostics.
func PinnedNodeName(binding string) string {
	name := ""
	managers.Range(func(key, _ any) bool {
		m, ok := key.(*Manager)
		if !ok {
			return true
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		for _, node := range m.saved.Nodes {
			if nodeBinding(node) == binding {
				name, _ = node["name"].(string)
				return false
			}
		}
		return true
	})
	return name
}

func (m *Manager) pinURL(secret, binding string) string {
	if m.pinnedProxyURL == "" {
		return pinURL(secret, binding)
	}
	u, _ := url.Parse(m.pinnedProxyURL)
	u.User = url.UserPassword(binding, pinPassword(secret, binding))
	return u.String()
}
