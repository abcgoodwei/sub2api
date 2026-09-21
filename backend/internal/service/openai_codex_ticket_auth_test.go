package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type harvestAuthRepo struct {
	AccountRepository
	account  Account
	disabled int
	reason   string
}

func (r *harvestAuthRepo) GetByID(context.Context, int64) (*Account, error) {
	a := r.account
	return &a, nil
}
func (r *harvestAuthRepo) ListByPlatform(context.Context, string) ([]Account, error) {
	return []Account{r.account}, nil
}
func (r *harvestAuthRepo) UpdateExtra(context.Context, int64, map[string]any) error { return nil }
func (r *harvestAuthRepo) SetError(_ context.Context, _ int64, reason string) error {
	r.disabled++
	r.reason = reason
	r.account.Status = StatusError
	r.account.Schedulable = false
	return nil
}

func TestCodexHarvestUnauthorizedDisablesBeforeParsingError(t *testing.T) {
	for _, status := range []int{401, 403, 407, 429, 503} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			repo := &harvestAuthRepo{account: *ticketTestAccount(19)}
			up := &httpUpstreamRecorder{responses: []*http.Response{{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"error":{"message":"test-only-invalid-token"}}`))}}}
			svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true, Models: []string{"gpt-6-astra"}, HarvestProxyURL: "http://test-proxy.invalid:31"}, up)
			svc.accountRepo = repo
			svc.refreshOpenAICodexTickets(context.Background())
			require.Equal(t, 1, len(up.requests))
			if status == 401 {
				require.Equal(t, 1, repo.disabled)
				require.False(t, repo.account.Schedulable)
				require.NotContains(t, repo.reason, "test-only-invalid-token")
				svc.openaiCodexTicketProbeCooldown.Delete(openAICodexTicketKey(19, "gpt-6-astra"))
				svc.refreshOpenAICodexTickets(context.Background())
				require.Equal(t, 1, len(up.requests), "error account must not re-enter future rounds")
			} else {
				require.Zero(t, repo.disabled)
			}
		})
	}
}
func TestCodexHarvestTerminalCredentialsOnly(t *testing.T) {
	for _, tt := range []struct {
		message  string
		terminal bool
	}{
		{"refresh_token_invalidated: secret response", true}, {"invalid_grant", true}, {"refresh_token_reused", true}, {"token_expired", true},
		{"invalid_client", false}, {"invalid_scope", false}, {"TLS handshake timeout", false}, {"invalid probe event", false}, {"upstream 503", false},
	} {
		t.Run(tt.message, func(t *testing.T) {
			repo := &harvestAuthRepo{account: *ticketTestAccount(19)}
			svc := &OpenAIGatewayService{accountRepo: repo}
			require.Equal(t, tt.terminal, svc.rejectCodexHarvestCredentials(&repo.account, 0, errors.New(tt.message)))
			if tt.terminal {
				require.Equal(t, 1, repo.disabled)
				require.NotContains(t, repo.reason, "secret response")
			} else {
				require.Zero(t, repo.disabled)
			}
		})
	}
}
func TestCodexManualHarvestStopsOnUnauthorized(t *testing.T) {
	repo := &harvestAuthRepo{account: *ticketTestAccount(19)}
	up := &httpUpstreamRecorder{responses: []*http.Response{{StatusCode: 401, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{}`))}}}
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true, HarvestProxyURL: "http://test-proxy.invalid:31"}, up)
	svc.accountRepo = repo
	err := svc.ExecuteManualHarvest(context.Background(), ManualHarvestRequest{AccountID: 19, MaxAttempts: 100}, func(ManualHarvestProgress) {})
	require.ErrorIs(t, err, errCodexHarvestCredentials)
	require.Equal(t, 1, len(up.requests))
	require.Equal(t, 1, repo.disabled)
}
