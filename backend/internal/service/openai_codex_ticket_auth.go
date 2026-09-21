package service

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"go.uber.org/zap"
)

var errCodexHarvestCredentials = errors.New("codex harvest credentials rejected; reauthorize the account")

// Configuration failures and transport errors are not evidence of a dead account.
// OAuthRefreshAPI has already attempted refresh-token race recovery before these
// errors reach the provider/harvester.
func isTerminalOpenAICredentialError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, code := range []string{"invalid_grant", "invalid_refresh_token", "refresh_token_invalidated", "refresh_token_reused", "app_session_terminated", "token_expired"} {
		if strings.Contains(msg, code) {
			return true
		}
	}
	return false
}

// SetError atomically changes status and schedulable and refreshes the scheduler
// snapshot. Do not persist raw OAuth responses, bearer tokens, or proxy errors.
func (s *OpenAIGatewayService) rejectCodexHarvestCredentials(account *Account, status int, tokenErr error, rejectedTokens ...string) bool {
	if status != http.StatusUnauthorized && !isTerminalOpenAICredentialError(tokenErr) {
		return false
	}
	if s.accountRepo != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var err error
		if repo, ok := s.accountRepo.(interface {
			SetOpenAIHarvestErrorIfCredentialsMatch(context.Context, int64, string, string, string) (bool, error)
		}); ok {
			rejectedToken := ""
			if len(rejectedTokens) > 0 {
				rejectedToken = rejectedTokens[0]
			}
			_, err = repo.SetOpenAIHarvestErrorIfCredentialsMatch(ctx, account.ID, rejectedToken, account.GetOpenAIRefreshToken(), errCodexHarvestCredentials.Error())
		} else {
			err = s.accountRepo.SetError(ctx, account.ID, errCodexHarvestCredentials.Error())
		}
		if err != nil {
			logger.L().Warn("openai_codex_ticket disable failed", zap.Int64("account_id", account.ID))
		}
	}
	return true
}
