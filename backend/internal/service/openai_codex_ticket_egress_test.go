package service

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/mihomo"
	"github.com/stretchr/testify/require"
)

func TestCodexTicketPinnedHTTPAndWSUseSameAccountExit(t *testing.T) {
	up := &httpUpstreamRecorder{resp: codexTicketResponse()}
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true, FailClosed: true, Models: []string{"gpt-6-astra"}, HarvestProxyURL: mihomo.Endpoint}, up)
	account := ticketTestAccount(25)
	proxy := &Proxy{ID: 5, Protocol: "http", Host: "ordinary.test", Port: 80, FallbackMode: FallbackModeDirect}
	account.Proxy = proxy
	account.ProxyID = &proxy.ID
	ticket := &openAICodexTicket{AccountID: 25, Model: "gpt-6-astra", State: fakeCodexTicketState(292), Length: 292, ExpiresAt: time.Now().Add(time.Hour), EgressNode: "node-25"}
	svc.openaiCodexTickets.Store(openAICodexTicketKey(25, "gpt-6-astra"), ticket)
	svc.codexTicketProxyResolver = func(id int64, node string) (string, error) {
		require.Equal(t, int64(25), id)
		require.Equal(t, "node-25", node)
		return "http://pinned.test:3102", nil
	}
	headers := http.Header{}
	require.NoError(t, svc.applyOpenAICodexTicket(context.Background(), account, "gpt-6-astra", headers))
	req, _ := http.NewRequest(http.MethodPost, "https://chatgpt.com/backend-api/codex/responses", nil)
	req.Header = headers
	_, err := svc.doOpenAIUpstream(req, proxy.URL(), account)
	require.NoError(t, err)
	require.Equal(t, "http://pinned.test:3102", up.lastProxyURL)
	wsProxy, err := svc.codexTicketWSProxyFactory(account)(context.Background(), headers, proxy.URL())
	require.NoError(t, err)
	require.Equal(t, up.lastProxyURL, wsProxy)
	bare, err := svc.codexTicketWSProxyFactory(account)(context.Background(), http.Header{}, proxy.URL())
	require.NoError(t, err)
	require.Equal(t, wsProxy, bare, "non-ticket models must share the account exit without borrowing another model's state")
	// A transport failure is attributed to the pinned exit, not the account's
	// ordinary proxy or direct networking, and never falls back to direct.
	up.err = errors.New("synthetic connection refused")
	beforeFailure := len(up.requests)
	_, err = svc.doOpenAIUpstream(req, proxy.URL(), account)
	require.Error(t, err)
	require.Len(t, up.requests, beforeFailure+1)
	proxyID, name := runtimeProxyErrorAttribution(account, err)
	require.Nil(t, proxyID)
	require.Equal(t, opsProxyNameCodexTicket, name)
	up.err = nil
	// Missing node blocks before sending; the account's direct fallback is not used.
	svc.codexTicketProxyResolver = func(int64, string) (string, error) { return "", errors.New("removed") }
	before := len(up.requests)
	_, err = svc.doOpenAIUpstream(req, proxy.URL(), account)
	require.True(t, IsOpenAITurnAdmissionError(err))
	require.Len(t, up.requests, before)
	require.True(t, svc.openAICodexTicketBlocksAccount(account, "gpt-6-astra"))
	_, err = svc.codexTicketWSProxyFactory(account)(context.Background(), headers, proxy.URL())
	require.True(t, IsOpenAITurnAdmissionError(err))
}
func TestCodexTicketManagedProxyRequiresReharvestOfLegacyTicket(t *testing.T) {
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true, FailClosed: true, HarvestProxyURL: mihomo.Endpoint}, nil)
	account := ticketTestAccount(25)
	attachReadyCodexTicket(account, "gpt-6-astra")
	require.True(t, svc.openAICodexTicketBlocksAccount(account, "gpt-6-astra"))
	require.Error(t, svc.applyOpenAICodexTicket(context.Background(), account, "gpt-6-astra", http.Header{}))
}

type codexPinnedDialer struct {
	calls int
	proxy string
	state string
}

func (d *codexPinnedDialer) Dial(_ context.Context, _ string, h http.Header, proxy string) (openAIWSClientConn, int, http.Header, error) {
	d.calls++
	d.proxy = proxy
	d.state = h.Get(openAICodexTurnStateHeader)
	return &openAIWSFakeConn{}, 101, http.Header{}, nil
}
func TestCodexTicketPinnedWSResolvesAfterFreshHandshakeHeaders(t *testing.T) {
	pool := newOpenAIWSConnPool(&config.Config{})
	defer pool.Close()
	dialer := &codexPinnedDialer{}
	pool.clientDialer = dialer
	req := openAIWSAcquireRequest{Account: ticketTestAccount(25), WSURL: "wss://example.test", Headers: http.Header{}, ProxyURL: "http://ordinary.test",
		HeadersFactory: func(_ context.Context, h http.Header) (http.Header, error) {
			h.Set(openAICodexTurnStateHeader, "fresh-state")
			return h, nil
		},
		ProxyURLFactory: func(_ context.Context, h http.Header, _ string) (string, error) {
			require.Equal(t, "fresh-state", h.Get(openAICodexTurnStateHeader))
			return "http://pinned.test", nil
		},
	}
	conn, err := pool.dialConn(context.Background(), req)
	require.NoError(t, err)
	defer conn.close()
	require.Equal(t, "http://pinned.test", dialer.proxy)
	require.Equal(t, "fresh-state", dialer.state)
	req.ProxyURLFactory = func(context.Context, http.Header, string) (string, error) {
		return "", denyOpenAITurn("ticket_egress_unavailable")
	}
	_, err = pool.dialConn(context.Background(), req)
	require.True(t, IsOpenAITurnAdmissionError(err))
	require.Equal(t, 1, dialer.calls)
}
