package service

import (
	"context"
	"net/http"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/mihomo"
)

func (s *OpenAIGatewayService) codexTicketEgressReady(ticket *openAICodexTicket) bool {
	if ticket == nil {
		return true
	}
	if ticket.EgressNode == "" {
		return s.openAICodexTicketHarvestProxyURL() != mihomo.Endpoint
	}
	_, err := s.resolveCodexTicketProxy(ticket.AccountID, ticket.EgressNode)
	return err == nil
}

// Match the state actually being sent, including a still-valid standby; looking
// up only the newest ticket could route an older handshake through a new exit.
func (s *OpenAIGatewayService) codexTicketEgress(ctx context.Context, account *Account, headers http.Header, fallback string) (string, bool, error) {
	if !s.openAICodexTicketEnabledContext(ctx) || !isOpenAICodexTicketAccount(account) {
		return fallback, false, nil
	}
	state := headers.Get(openAICodexTurnStateHeader)
	if state == "" {
		// Other business models share the account's live exit, without borrowing
		// a model-specific ticket or adding a turn-state header to that request.
		for _, model := range s.openAICodexTicketConfig().Models {
			ticket := s.lookupOpenAICodexTicket(account, model)
			if ticket == nil || ticket.EgressNode == "" || !ticket.valid(time.Now(), openAICodexTicketTargetLength(account, s.openAICodexTicketConfig())) {
				continue
			}
			proxy, err := s.resolveCodexTicketProxy(account.ID, ticket.EgressNode)
			if err != nil {
				return "", true, denyOpenAITurn("ticket_egress_unavailable")
			}
			return proxy, true, nil
		}
		return fallback, false, nil
	}
	for _, model := range s.openAICodexTicketConfig().Models {
		ticket := s.lookupOpenAICodexTicket(account, model)
		if ticket == nil {
			continue
		}
		for _, candidate := range []*openAICodexTicket{ticket, ticket.Standby} {
			if candidate == nil || candidate.State != state || candidate.EgressNode == "" {
				continue
			}
			if !candidate.valid(time.Now(), openAICodexTicketTargetLength(account, s.openAICodexTicketConfig())) {
				return "", true, denyOpenAITicket()
			}
			proxy, err := s.resolveCodexTicketProxy(account.ID, candidate.EgressNode)
			if err != nil {
				return "", true, denyOpenAITurn("ticket_egress_unavailable")
			}
			return proxy, true, nil
		}
	}
	if s.openAICodexTicketHarvestProxyURLContext(ctx) == mihomo.Endpoint {
		return "", true, denyOpenAITicket()
	}
	return fallback, false, nil
}

func (s *OpenAIGatewayService) codexTicketWSProxyFactory(account *Account) func(context.Context, http.Header, string) (string, error) {
	return func(ctx context.Context, headers http.Header, fallback string) (string, error) {
		proxy, _, err := s.codexTicketEgress(ctx, account, headers, fallback)
		return proxy, err
	}
}

func (s *OpenAIGatewayService) resolveCodexTicketProxy(accountID int64, node string) (string, error) {
	if s.codexTicketProxyResolver != nil {
		return s.codexTicketProxyResolver(accountID, node)
	}
	return mihomo.PinnedProxy(accountID, node)
}
