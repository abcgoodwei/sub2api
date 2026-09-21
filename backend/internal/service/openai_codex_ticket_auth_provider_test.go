//go:build unit

package service

import (
	"context"
	"errors"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestCodexHarvestProviderDoesNotHideTerminalRefreshFailure(t *testing.T) {
	account := ticketTestAccount(19)
	account.Credentials["refresh_token"] = "synthetic-refresh"
	account.Credentials["expires_at"] = time.Now().Add(-time.Hour).Format(time.RFC3339)
	repo := &refreshAPIAccountRepo{account: account}
	executor := &refreshAPIExecutorStub{needsRefresh: true, err: errors.New("refresh_token_invalidated")}
	provider := NewOpenAITokenProvider(repo, nil, nil)
	provider.refreshAPI = NewOAuthRefreshAPI(repo, nil)
	provider.executor = executor
	token, err := provider.GetAccessToken(context.Background(), account)
	require.Error(t, err)
	require.Empty(t, token)
	require.True(t, isTerminalOpenAICredentialError(err))
	executor.err = errors.New("TLS handshake timeout")
	token, err = provider.GetAccessToken(context.Background(), account)
	require.NoError(t, err)
	require.Equal(t, "tok", token, "transient failures keep the existing provider policy")
}
