package service

import (
	"context"
	"errors"
	"github.com/Wei-Shaw/sub2api/internal/mihomo"
	"net/http"
	"time"
)

var ErrCodexTicketResponseRejected = errors.New("ticket response rejected; upstream may have charged; request was not replayed")

func (s *OpenAIGatewayService) codexTicketRequestBound(req *http.Request, account *Account) bool {
	if s == nil || req == nil || account == nil || !s.openAICodexTicketEnabledContext(req.Context()) {
		return false
	}
	sent := req.Header.Get(openAICodexTurnStateHeader)
	if sent == "" {
		return false
	}
	for _, model := range s.openAICodexTicketConfig().Models {
		ticket := s.lookupOpenAICodexTicket(account, model)
		if ticket != nil && ticket.State == sent && !ticket.Revoked {
			return true
		}
	}
	return false
}

func codexResponseMismatches(req *http.Request, resp *http.Response, account *Account) bool {
	if req == nil || resp == nil || account == nil || resp.StatusCode != http.StatusOK || req.Header.Get(openAICodexTurnStateHeader) == "" {
		return false
	}
	returned := extractOpenAICodexTurnState(resp.Header)
	if returned == "" {
		return false
	}
	shape, err := parseOpenAICodexTicketShape(returned)
	return err != nil || shape.Blocks != openAICodexTicketExpectedBlocks(account) || shape.IssuedAt.After(time.Now().Add(30*time.Second)) || !time.Now().Before(shape.IssuedAt.Add(time.Hour-30*time.Second))
}

// A mismatch invalidates only the ticket actually sent by this request. The
// current response continues through normal accounting; never replay it here.
func (s *OpenAIGatewayService) observeCodexTicketResponse(req *http.Request, resp *http.Response, account *Account) {
	if s == nil || req == nil || resp == nil || account == nil || resp.StatusCode != http.StatusOK {
		return
	}
	sent := req.Header.Get(openAICodexTurnStateHeader)
	returned := extractOpenAICodexTurnState(resp.Header)
	if sent == "" || returned == "" || !s.openAICodexTicketEnabledContext(req.Context()) {
		return
	}
	shape, err := parseOpenAICodexTicketShape(returned)
	if err == nil && shape.Blocks == openAICodexTicketExpectedBlocks(account) && !shape.IssuedAt.After(time.Now().Add(30*time.Second)) && time.Now().Before(shape.IssuedAt.Add(time.Hour-30*time.Second)) {
		return
	}
	s.openaiCodexTicketStateMu.Lock()
	defer s.openaiCodexTicketStateMu.Unlock()
	cfg := s.openAICodexTicketConfig()
	for _, model := range cfg.Models {
		current := s.lookupCodexTicketLocked(account, model)
		if current == nil || current.State != sent || current.Revoked {
			continue
		}
		if current.EgressNode != "" {
			ctx, cancel := context.WithTimeout(context.WithoutCancel(req.Context()), time.Second)
			mihomo.ReleaseAccountBinding(ctx, account.ID, current.EgressNode)
			cancel()
		}
		next := *current
		if current.Standby.valid(time.Now(), openAICodexTicketTargetLength(account, cfg)) {
			next = *current.Standby
		} else {
			next.Revoked = true
		}
		next.CapturedAt = time.Now()
		s.openaiCodexTickets.Store(openAICodexTicketKey(account.ID, model), &next)
		recordCodexHarvestTicketReject(account, model, "response_mismatch", len(returned), shape.Blocks)
		if s.accountRepo != nil {
			ctx, cancel := context.WithTimeout(context.WithoutCancel(req.Context()), time.Second)
			_ = s.accountRepo.UpdateExtra(ctx, account.ID, map[string]any{openAICodexTicketExtraKey(model): &next})
			cancel()
		}
	}
}
