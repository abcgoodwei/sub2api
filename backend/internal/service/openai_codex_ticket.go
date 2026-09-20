package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Wei-Shaw/sub2api/internal/mihomo"
	"io"
	"maps"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/google/uuid"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
)

const (
	openAICodexTicketExtraKeyPrefix  = "codex_turn_ticket:"
	OpenAICodexSkipHarvestExtraKey   = "codex_skip_harvest"
	openAICodexAstraMinVersion       = "0.153.4"
	openAICodexTicketStatePrefix     = "gAAAAA"
	openAICodexTicketDefaultModel    = "gpt-6-astra"
	openAICodexTicketDefaultSolModel = "gpt-5.6-sol"
	openAICodexTicketPersonalBlocks  = 10
	openAICodexTicketTeamBlocks      = 12
)

// ErrOpenAICodexTicketUnavailable 表示该号该模型没有可用的 292 门票，
// 且 fail_closed 禁止裸打业务请求。
var ErrOpenAICodexTicketUnavailable = errors.New("codex turn-state ticket unavailable")

type openAICodexTicket struct {
	AccountID  int64              `json:"account_id"`
	Model      string             `json:"model"`
	State      string             `json:"state"`
	Length     int                `json:"length"`
	CapturedAt time.Time          `json:"captured_at"`
	ExpiresAt  time.Time          `json:"expires_at"`
	Attempts   int                `json:"attempts"`
	Blocks     int                `json:"blocks,omitempty"`
	IssuedAt   time.Time          `json:"issued_at,omitempty"`
	Identity   string             `json:"identity,omitempty"`
	Standby    *openAICodexTicket `json:"standby,omitempty"`
	Revoked    bool               `json:"revoked,omitempty"`
}

type openAICodexTicketShape struct {
	Blocks   int
	IssuedAt time.Time
}

func parseOpenAICodexTicketShape(value string) (openAICodexTicketShape, error) {
	value = strings.TrimSpace(value)
	if len(value) > 2048 || strings.ContainsAny(value, "\r\n\t ") {
		return openAICodexTicketShape{}, errors.New("invalid state encoding")
	}
	core := strings.TrimRight(value, "=")
	if len(value)-len(core) > 2 {
		return openAICodexTicketShape{}, errors.New("invalid state padding")
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(core)
	if err != nil || len(raw) < 73 || raw[0] != 0x80 || (len(raw)-57)%16 != 0 {
		return openAICodexTicketShape{}, errors.New("unrecognized state envelope")
	}
	issuedUnix := binary.BigEndian.Uint64(raw[1:9])
	if issuedUnix < 1577836800 || issuedUnix >= 4102444800 {
		return openAICodexTicketShape{}, errors.New("state timestamp out of range")
	}
	return openAICodexTicketShape{Blocks: (len(raw) - 57) / 16, IssuedAt: time.Unix(int64(issuedUnix), 0)}, nil
}

// openAICodexTicketTeamPlanMarkers identify ChatGPT Team/Business/Enterprise
// subscriptions. Upstream does not report a single canonical plan_type: besides
// "team" it also emits variants such as "self_serve_business_prolite", so these
// plans are detected by substring instead of equality.
var openAICodexTicketTeamPlanMarkers = []string{"team", "business", "enterprise"}

func openAICodexTicketIsTeamPlan(account *Account) bool {
	if account == nil {
		return false
	}
	plan := strings.ToLower(strings.TrimSpace(account.GetCredential("plan_type")))
	if plan == "" {
		return false
	}
	for _, marker := range openAICodexTicketTeamPlanMarkers {
		if strings.Contains(plan, marker) {
			return true
		}
	}
	return false
}

func openAICodexTicketExpectedBlocks(account *Account) int {
	if openAICodexTicketIsTeamPlan(account) {
		return openAICodexTicketTeamBlocks
	}
	return openAICodexTicketPersonalBlocks
}

func openAICodexTicketExpectedLength(account *Account) int {
	return base64.URLEncoding.EncodedLen(57 + 16*openAICodexTicketExpectedBlocks(account))
}

func openAICodexTicketTargetLength(account *Account, cfg config.OpenAICodexTicketConfig) int {
	if cfg.TargetLength > 0 && cfg.TargetLength != 292 {
		return cfg.TargetLength
	}
	return openAICodexTicketExpectedLength(account)
}

func openAICodexTicketKey(accountID int64, model string) string {
	return fmt.Sprintf("%d\x00%s", accountID, strings.TrimSpace(model))
}

func openAICodexTicketExtraKey(model string) string {
	return openAICodexTicketExtraKeyPrefix + strings.TrimSpace(model)
}

func normalizeOpenAICodexTicketModel(model string) string {
	return strings.TrimSpace(model)
}

// NormalizeOpenAICodexTicketModels removes empty/duplicate model names while
// preserving the configured order. An empty result is meaningful: it disables
// ticket gating for every model while leaving the global feature enabled.
func NormalizeOpenAICodexTicketModels(models []string) []string {
	seen := make(map[string]struct{}, len(models))
	out := make([]string, 0, len(models))
	for _, model := range models {
		model = normalizeOpenAICodexTicketModel(model)
		if model == "" {
			continue
		}
		if _, ok := seen[model]; ok {
			continue
		}
		seen[model] = struct{}{}
		out = append(out, model)
	}
	return out
}

func extractOpenAICodexTicketModel(body []byte) string {
	return normalizeOpenAICodexTicketModel(gjson.GetBytes(body, "model").String())
}

func (s *OpenAIGatewayService) openAICodexTicketConfig() config.OpenAICodexTicketConfig {
	cfg := config.OpenAICodexTicketConfig{}
	if s != nil && s.cfg != nil {
		cfg = s.cfg.Gateway.OpenAICodexTicket
	}
	if cfg.TargetLength <= 0 {
		cfg.TargetLength = 292
	}
	if cfg.TTLSeconds <= 0 {
		cfg.TTLSeconds = 3600
	}
	if cfg.RefreshBeforeSeconds <= 0 {
		cfg.RefreshBeforeSeconds = 600
	}
	if cfg.HarvestProbeIntervalSeconds < 30 {
		cfg.HarvestProbeIntervalSeconds = 180
	}
	if cfg.HarvestCooldownSeconds <= 0 {
		cfg.HarvestCooldownSeconds = 180
	}
	if cfg.MaxProbesPerRound <= 0 {
		cfg.MaxProbesPerRound = 6
	}
	if cfg.HarvestAttemptTimeoutSeconds <= 0 {
		cfg.HarvestAttemptTimeoutSeconds = 25
	}
	if len(cfg.Models) == 0 {
		cfg.Models = []string{openAICodexTicketDefaultModel, openAICodexTicketDefaultSolModel}
	}
	if s != nil && s.settingService != nil {
		ctx := context.Background()
		cfg.Models = s.settingService.GetOpenAICodexTicketModels(ctx, cfg.Models)
		cfg.FailClosed = s.settingService.GetOpenAICodexTicketFailClosed(ctx)
		cfg.HarvestProbeIntervalSeconds = s.settingService.GetOpenAICodexTicketProbeIntervalSeconds(ctx, cfg.HarvestProbeIntervalSeconds)
		cfg.HarvestCooldownSeconds = s.settingService.GetOpenAICodexTicketCooldownSeconds(ctx, cfg.HarvestCooldownSeconds)
		cfg.MaxProbesPerRound = s.settingService.GetOpenAICodexTicketMaxProbesPerRound(ctx, cfg.MaxProbesPerRound)
		cfg.HarvestAttemptTimeoutSeconds = s.settingService.GetOpenAICodexTicketAttemptTimeoutSeconds(ctx, cfg.HarvestAttemptTimeoutSeconds)
		cfg.RefreshBeforeSeconds = s.settingService.GetOpenAICodexTicketRefreshBeforeSeconds(ctx, cfg.RefreshBeforeSeconds)
	}
	return cfg
}

func (s *OpenAIGatewayService) openAICodexTicketGatedModel(model string) bool {
	model = normalizeOpenAICodexTicketModel(model)
	if model == "" || !s.openAICodexTicketEnabled() {
		return false
	}
	for _, item := range s.openAICodexTicketConfig().Models {
		if normalizeOpenAICodexTicketModel(item) == model {
			return true
		}
	}
	return false
}

// OpenAICodexTicketStatus 是给管理端看的门票摘要，不含 state blob。
type OpenAICodexTicketStatus struct {
	Model            string             `json:"model"`
	Length           int                `json:"length,omitempty"`
	Ready            bool               `json:"ready"`
	RemainingSeconds int64              `json:"remaining_seconds"`
	Blocked          bool               `json:"blocked"`
	ExpiresAt        *time.Time         `json:"expires_at,omitempty"`
	StandbyExpiresAt *time.Time         `json:"standby_expires_at,omitempty"`
	Probe            *CodexProbeSummary `json:"probe,omitempty"`
}

func OpenAICodexTicketStatuses(account *Account, cfg config.OpenAICodexTicketConfig, now time.Time) []OpenAICodexTicketStatus {
	if !cfg.Enabled || !isOpenAICodexTicketAccount(account) {
		return nil
	}
	models, targetLen := cfg.Models, openAICodexTicketTargetLength(account, cfg)
	if models == nil {
		models = []string{openAICodexTicketDefaultModel, openAICodexTicketDefaultSolModel}
	}
	if targetLen <= 0 {
		targetLen = 292
	}
	out := make([]OpenAICodexTicketStatus, 0, len(models))
	for _, model := range models {
		model = normalizeOpenAICodexTicketModel(model)
		if model == "" {
			continue
		}
		status := OpenAICodexTicketStatus{Model: model}
		status.Probe = readCodexProbe(account, model)
		ticket := parseOpenAICodexTicketFromAny(0, model, nil)
		if account != nil && account.Extra != nil {
			ticket = parseOpenAICodexTicketFromAny(account.ID, model, account.Extra[openAICodexTicketExtraKey(model)])
		}
		if ticket != nil && !ticketIdentityMatches(account, ticket) {
			ticket = nil
		}
		if ticket != nil && ticket.Standby.valid(now, targetLen) {
			exp := ticket.Standby.ExpiresAt
			status.StandbyExpiresAt = &exp
			if !ticket.valid(now, targetLen) {
				ticket = ticket.Standby
				status.StandbyExpiresAt = nil
			}
		}
		if ticket.valid(now, targetLen) {
			status.Ready = true
			status.Length = ticket.Length
			remaining := int64(ticket.ExpiresAt.Sub(now) / time.Second)
			if remaining < 0 {
				remaining = 0
			}
			status.RemainingSeconds = remaining
			exp := ticket.ExpiresAt
			status.ExpiresAt = &exp
		}
		status.Blocked = cfg.FailClosed && !status.Ready
		out = append(out, status)
	}
	return out
}

func (s *OpenAIGatewayService) openAICodexTicketEnabled() bool {
	return s.openAICodexTicketEnabledContext(context.Background())
}

func (s *OpenAIGatewayService) openAICodexTicketEnabledContext(ctx context.Context) bool {
	if s == nil {
		return false
	}
	fallback := s.cfg != nil && s.cfg.Gateway.OpenAICodexTicket.Enabled
	if s.settingService != nil {
		return s.settingService.GetOpenAICodexTicketEnabled(ctx, fallback)
	}
	return fallback
}

func (s *OpenAIGatewayService) openAICodexTicketHarvestProxyURL() string {
	return s.openAICodexTicketHarvestProxyURLContext(context.Background())
}

func (s *OpenAIGatewayService) openAICodexTicketHarvestProxyURLContext(ctx context.Context) string {
	if s.settingService != nil {
		if proxy := s.settingService.GetOpenAICodexTicketHarvestProxyURL(ctx); proxy != "" {
			return proxy
		}
	}
	return strings.TrimSpace(s.openAICodexTicketConfig().HarvestProxyURL)
}

func (t *openAICodexTicket) valid(now time.Time, targetLen int) bool {
	if t != nil && t.Revoked {
		return false
	}
	if t == nil {
		return false
	}
	state := strings.TrimSpace(t.State)
	if len(state) != targetLen || t.Length != targetLen || !strings.HasPrefix(state, openAICodexTicketStatePrefix) {
		return false
	}
	if t.ExpiresAt.IsZero() || !now.Before(t.ExpiresAt) {
		return false
	}
	if !t.IssuedAt.IsZero() && (t.IssuedAt.After(now.Add(30*time.Second)) || !now.Before(t.IssuedAt.Add(time.Hour-30*time.Second))) {
		return false
	}
	return true
}

func (t *openAICodexTicket) needsRefresh(now time.Time, refreshBefore time.Duration) bool {
	if t == nil || t.ExpiresAt.IsZero() {
		return true
	}
	return !t.ExpiresAt.After(now.Add(refreshBefore))
}

func (s *OpenAIGatewayService) lookupOpenAICodexTicket(account *Account, model string) *openAICodexTicket {
	if s == nil {
		return nil
	}
	s.openaiCodexTicketStateMu.Lock()
	defer s.openaiCodexTicketStateMu.Unlock()
	return s.lookupCodexTicketLocked(account, model)
}

// Keep the production identity binding: candidates are hydrated by the
// scheduler before admission, so missing identity must not bypass the gate.
func ticketIdentity(account *Account) string {
	if account == nil {
		return ""
	}
	return fmt.Sprintf("%x", sha256.Sum256([]byte(account.GetCredential("chatgpt_account_id")+"\x00"+account.GetCredential("email"))))
}
func ticketIdentityMatches(account *Account, ticket *openAICodexTicket) bool {
	return ticket != nil && (ticket.Identity == "" || ticket.Identity == ticketIdentity(account))
}

func (s *OpenAIGatewayService) lookupCodexTicketLocked(account *Account, model string) *openAICodexTicket {
	if s == nil || account == nil || account.ID <= 0 {
		return nil
	}
	model = normalizeOpenAICodexTicketModel(model)
	if model == "" {
		return nil
	}
	key := openAICodexTicketKey(account.ID, model)
	targetLen := openAICodexTicketTargetLength(account, s.openAICodexTicketConfig())
	now := time.Now()
	var mem *openAICodexTicket
	if raw, ok := s.openaiCodexTickets.Load(key); ok {
		mem, _ = raw.(*openAICodexTicket)
	}
	var extra *openAICodexTicket
	if account.Extra != nil {
		extra = parseOpenAICodexTicketFromAny(account.ID, model, account.Extra[openAICodexTicketExtraKey(model)])
	}
	if mem != nil && !ticketIdentityMatches(account, mem) {
		mem = nil
	}
	if extra != nil && !ticketIdentityMatches(account, extra) {
		extra = nil
	}
	if extra != nil && !extra.valid(now, targetLen) && extra.Standby.valid(now, targetLen) {
		extra = extra.Standby
	}
	// The cached tombstone wins over stale account snapshots after invalidation.
	if mem != nil && mem.Revoked {
		return mem
	}
	if mem != nil && !mem.valid(now, targetLen) && mem.Standby.valid(now, targetLen) {
		promoted := *mem.Standby
		s.openaiCodexTickets.Store(key, &promoted)
		return &promoted
	}
	if extra.valid(now, targetLen) && (mem == nil || extra.CapturedAt.After(mem.CapturedAt)) {
		s.openaiCodexTickets.Store(key, extra)
		return extra
	}
	if mem.valid(now, targetLen) {
		return mem
	}
	if extra != nil {
		s.openaiCodexTickets.Store(key, extra)
		return extra
	}
	if mem != nil {
		s.openaiCodexTickets.Delete(key)
	}
	return nil
}

func parseOpenAICodexTicketFromAny(accountID int64, model string, raw any) *openAICodexTicket {
	if raw == nil {
		return nil
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var ticket openAICodexTicket
	if err := json.Unmarshal(b, &ticket); err != nil {
		return nil
	}
	ticket.AccountID = accountID
	if strings.TrimSpace(model) != "" {
		ticket.Model = model
	}
	ticket.State = strings.TrimSpace(ticket.State)
	if ticket.Length == 0 {
		ticket.Length = len(ticket.State)
	}
	if ticket.State == "" {
		return nil
	}
	return &ticket
}

func (s *OpenAIGatewayService) storeOpenAICodexTicket(ctx context.Context, account *Account, ticket *openAICodexTicket) {
	if s == nil || account == nil || ticket == nil || account.ID <= 0 {
		return
	}
	incoming := *ticket
	standby := false
	s.openaiCodexTicketStateMu.Lock()
	defer s.openaiCodexTicketStateMu.Unlock()
	model := normalizeOpenAICodexTicketModel(ticket.Model)
	ticket.Identity = ticketIdentity(account)
	if current := s.lookupCodexTicketLocked(account, model); current.valid(time.Now(), openAICodexTicketTargetLength(account, s.openAICodexTicketConfig())) && current.State != ticket.State {
		copy := *current
		copy.Standby = ticket
		ticket = &copy
		standby = true
	}
	ticket.Model = model
	ticket.AccountID = account.ID
	s.openaiCodexTickets.Store(openAICodexTicketKey(account.ID, model), ticket)
	recordCodexHarvestTicketStore(account, &incoming, standby)
	if s.accountRepo == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := s.accountRepo.UpdateExtra(ctx, account.ID, map[string]any{
		openAICodexTicketExtraKey(model): ticket,
	}); err != nil {
		logger.L().Warn("openai_codex_ticket persist failed",
			zap.Int64("account_id", account.ID),
			zap.String("model", model),
			zap.Error(err),
		)
	}
}

// applyOpenAICodexTicket 在出站请求上覆盖 x-codex-turn-state。
// 请求路径只注入已捕获的有效门票，不现场打票；无票则返回
// ErrOpenAICodexTicketUnavailable。打票由后台 harvester 完成。
func (s *OpenAIGatewayService) applyOpenAICodexTicket(ctx context.Context, account *Account, model string, h http.Header) error {
	if s == nil || h == nil || !isOpenAICodexTicketAccount(account) || !s.openAICodexTicketEnabledContext(ctx) {
		return nil
	}
	model = normalizeOpenAICodexTicketModel(model)
	if model == "" || !s.openAICodexTicketGatedModel(model) {
		return nil
	}
	cfg := s.openAICodexTicketConfig()
	if s.openAICodexTicketHarvestExcluded(account) {
		if !cfg.FailClosed {
			return nil
		}
		return ErrOpenAICodexTicketUnavailable
	}
	ticket := s.lookupOpenAICodexTicket(account, model)
	if ticket.valid(time.Now(), openAICodexTicketTargetLength(account, cfg)) {
		h.Set(openAICodexTurnStateHeader, ticket.State)
		return nil
	}
	if !cfg.FailClosed {
		return nil
	}
	return ErrOpenAICodexTicketUnavailable
}

// openAICodexTicketOutboundModel 预测本请求真正出站的模型名，也就是
// applyOpenAICodexTicket 注入时读到的 body.model。
//
// 调度门控与注入必须按同一个模型名判定门票。普通请求下二者同源：Forward 的
// upstreamModel 与本函数都走 resolveOpenAIAccountUpstreamModelForRequest，且
// Forward 会把 body.model 改写成该值后才注入。但 /responses/compact 例外——
// Forward 会把出站模型进一步改写为 compact 映射或 gateway.openai_compact_model
// （默认非空），此时若门控仍按客户端原始模型判定，就会把「实际出站是非门控
// 模型、根本不需要票」的 compact 请求整片误拦成不可调度。
func (s *OpenAIGatewayService) openAICodexTicketOutboundModel(account *Account, requestedModel string, requireCompact bool) string {
	model := strings.TrimSpace(requestedModel)
	if account == nil || model == "" {
		return model
	}
	if !account.IsOpenAI() {
		return canonicalOpenAIAccountSchedulingModel(account, model)
	}
	_, upstreamModel := resolveOpenAIForwardMappedModels(account, model, requireCompact)
	if requireCompact {
		// 与 Forward 同序：compact 兜底模型优先于普通/compact 映射结果。
		if compactModel := strings.TrimSpace(s.resolveOpenAICompactFallbackModel(account, model)); compactModel != "" {
			upstreamModel = compactModel
		}
	}
	if upstreamModel = strings.TrimSpace(upstreamModel); upstreamModel != "" {
		return upstreamModel
	}
	return model
}

// outboundModel 必须是真正会发给上游的模型名（openAICodexTicketOutboundModel），
// 不是客户端原始模型：注入侧读的是出站 body.model，两侧口径必须一致。
func (s *OpenAIGatewayService) openAICodexTicketBlocksAccount(account *Account, outboundModel string) bool {
	if s == nil || !isOpenAICodexTicketAccount(account) || !s.openAICodexTicketEnabled() {
		return false
	}
	cfg := s.openAICodexTicketConfig()
	if !cfg.FailClosed {
		return false
	}
	model := normalizeOpenAICodexTicketModel(outboundModel)
	if !s.openAICodexTicketGatedModel(model) {
		return false
	}
	if s.openAICodexTicketHarvestExcluded(account) {
		return true
	}
	ticket := s.lookupOpenAICodexTicket(account, model)
	return !ticket.valid(time.Now(), openAICodexTicketTargetLength(account, cfg))
}

func (s *OpenAIGatewayService) openAICodexTicketReadyForRequest(account *Account, requestedModel string, requireCompact bool) bool {
	if s == nil || account == nil || !isOpenAICodexTicketAccount(account) || !s.openAICodexTicketEnabled() {
		return false
	}
	outbound := s.openAICodexTicketOutboundModel(account, requestedModel, requireCompact)
	model := normalizeOpenAICodexTicketModel(outbound)
	if model == "" || !s.openAICodexTicketGatedModel(model) {
		return false
	}
	if s.openAICodexTicketHarvestExcluded(account) {
		return false
	}
	ticket := s.lookupOpenAICodexTicket(account, model)
	return ticket.valid(time.Now(), openAICodexTicketTargetLength(account, s.openAICodexTicketConfig()))
}

func extraTruthy(value any) bool {
	switch v := value.(type) {
	case bool:
		return v
	case string:
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "1", "true", "yes":
			return true
		}
	case float64:
		return v != 0
	case int:
		return v != 0
	case int64:
		return v != 0
	}
	return false
}

func openAICodexSkipHarvest(account *Account) bool {
	if account == nil || account.Extra == nil {
		return false
	}
	return extraTruthy(account.Extra[OpenAICodexSkipHarvestExtraKey])
}

// Harvest-excluded accounts keep leftover Extra tickets, but fail-closed
// gated routing must not spend them. Group scope and per-account skip_harvest
// share this gate so a 5x in the same assistant group cannot consume tickets
// harvested for 20x.
func (s *OpenAIGatewayService) openAICodexTicketHarvestExcluded(account *Account) bool {
	if openAICodexSkipHarvest(account) {
		return true
	}
	if s == nil || s.settingService == nil || account == nil {
		return false
	}
	scope, err := s.settingService.GetCodexTicketHarvestScope(context.Background())
	if err != nil {
		return true
	}
	if scope.Mode != "selected" {
		return false
	}
	return !scope.includes(account)
}

// Sticky sessions ignore account priority. A leftover 292 on a low-priority
// account (for example a 5x that is no longer harvested) would otherwise pin
// traffic forever even after a higher-priority account such as 20x harvests.
func (s *OpenAIGatewayService) openAICodexTicketShouldYieldStickyTo(sticky *Account, candidates []*Account, requestedModel string, requireCompact bool, excludedIDs map[int64]struct{}) bool {
	if s == nil || sticky == nil || !s.openAICodexTicketEnabled() {
		return false
	}
	outbound := s.openAICodexTicketOutboundModel(sticky, requestedModel, requireCompact)
	if !s.openAICodexTicketGatedModel(outbound) {
		return false
	}
	stickyPriority := openAIAccountSchedulingPriority(sticky)
	for _, account := range candidates {
		if account == nil || account.ID == sticky.ID {
			continue
		}
		if excludedIDs != nil {
			if _, skipped := excludedIDs[account.ID]; skipped {
				continue
			}
		}
		if openAIAccountSchedulingPriority(account) >= stickyPriority {
			continue
		}
		if s.isOpenAIAccountRequestRuntimeBlocked(account, requestedModel, requireCompact) {
			continue
		}
		if !s.openAICodexTicketReadyForRequest(account, requestedModel, requireCompact) {
			continue
		}
		recordCodexHarvestSelect(sticky, requestedModel, "yield", "higher_priority_ticket", account.Name)
		return true
	}
	return false
}

func (s *OpenAIGatewayService) openAICodexTicketShouldYieldSticky(ctx context.Context, sticky *Account, groupID *int64, platform, requestedModel string, requireCompact bool, excludedIDs map[int64]struct{}) bool {
	if s == nil || sticky == nil || !s.openAICodexTicketEnabled() {
		return false
	}
	accounts, err := s.listSchedulableAccountsForRequest(ctx, groupID, platform, requestedModel, requireCompact, excludedIDs)
	if err != nil || len(accounts) == 0 {
		return false
	}
	candidates := make([]*Account, 0, len(accounts))
	for i := range accounts {
		candidates = append(candidates, &accounts[i])
	}
	return s.openAICodexTicketShouldYieldStickyTo(sticky, candidates, requestedModel, requireCompact, excludedIDs)
}

func (s *OpenAIGatewayService) fireOpenAICodexTicketProbe(ctx context.Context, account *Account, token, model, proxyURL string, attemptTimeout time.Duration) (state string, status int, err error) {
	// Queueing behind another account must not consume this probe's upstream
	// timeout; every selected account receives a full bounded attempt.
	releaseNode, leaseErr := mihomo.Lease(ctx, proxyURL)
	if leaseErr != nil {
		return "", 0, leaseErr
	}
	defer func() { releaseNode(err == nil && status == http.StatusOK) }()
	attemptCtx, cancel := context.WithTimeout(ctx, attemptTimeout)
	defer cancel()

	body := []byte(`{"model":` + jsonString(model) + `,"store":false,"stream":true,"instructions":"Reply with exactly: pong","input":[{"role":"user","content":[{"type":"input_text","text":"ping"}]}]}`)
	req, err := http.NewRequestWithContext(attemptCtx, http.MethodPost, chatgptCodexURL, bytes.NewReader(body))
	if err != nil {
		return "", 0, err
	}
	req = req.WithContext(WithHTTPUpstreamProfile(req.Context(), HTTPUpstreamProfileOpenAIHarvest))
	req.Close = true
	req.Host = "chatgpt.com"
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("OpenAI-Beta", "responses=experimental")
	req.Header.Set("session_id", uuid.NewString())
	if err := resolveAndSetOpenAIChatGPTAccountHeaders(attemptCtx, s.accountRepo, req.Header, account); err != nil {
		return "", 0, err
	}
	applyOpenAICodexTicketHarvestIdentity(req.Header, model)

	// Synthetic probes must use the dedicated no-reuse transport even when the
	// production account is bound to a plugin. This also avoids reading pluginManager
	// while handlers are still wiring it during gateway construction.
	resp, err := s.httpUpstream.Do(req, proxyURL, account.ID, account.Concurrency)
	if err != nil {
		return "", 0, err
	}
	if resp == nil {
		return "", 0, errors.New("nil upstream response")
	}
	// Accept the header only after the bounded response completes successfully.
	defer func() {
		if resp.Body != nil {
			_ = resp.Body.Close()
		}
	}()
	state = extractOpenAICodexTurnState(resp.Header)
	if resp.Body != nil {
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
		if readErr != nil || len(body) > 1<<20 {
			return state, resp.StatusCode, errors.New("probe response incomplete")
		}
		if err := validateCodexProbeResponse(body); err != nil {
			return state, resp.StatusCode, err
		}
	} else {
		return state, resp.StatusCode, errors.New("probe response body missing")
	}
	return state, resp.StatusCode, nil
}

func (s *OpenAIGatewayService) ticketProbeCoolingDown(accountID int64, model string, now time.Time) bool {
	if s == nil {
		return false
	}
	key := openAICodexTicketKey(accountID, model)
	value, _ := s.openaiCodexTicketProbeCooldown.Load(key)
	until, ok := value.(time.Time)
	if !ok || !until.After(now) {
		s.openaiCodexTicketProbeCooldown.Delete(key)
		return false
	}
	return true
}

func (s *OpenAIGatewayService) cooldownTicketProbe(accountID int64, model string, cfg config.OpenAICodexTicketConfig) {
	if s == nil {
		return
	}
	s.openaiCodexTicketProbeCooldown.Store(openAICodexTicketKey(accountID, model), time.Now().Add(time.Duration(cfg.HarvestCooldownSeconds)*time.Second))
}

func jsonString(v string) string {
	b, err := json.Marshal(v)
	if err != nil {
		return `""`
	}
	return string(b)
}

func applyOpenAICodexTicketHarvestIdentity(h http.Header, model string) {
	ensureCodexIdentityHeaders(h)
	enforceCodexIdentityHeaders(h)
	version := strings.TrimSpace(h.Get("version"))
	if needsOpenAICodexAstraVersion(model) && (version == "" || CompareVersions(version, openAICodexAstraMinVersion) < 0) {
		h.Set("version", openAICodexAstraMinVersion)
		h.Set("user-agent", buildCodexCLIUserAgent(openAICodexAstraMinVersion))
		h.Set("originator", openai.CodexDefaultOriginator)
	}
}

func needsOpenAICodexAstraVersion(model string) bool {
	m := strings.ToLower(normalizeOpenAICodexTicketModel(model))
	return strings.Contains(m, "gpt-6") || strings.Contains(m, "astra")
}

func (s *OpenAIGatewayService) StartOpenAICodexTicketHarvester() {
	if s == nil {
		return
	}
	s.openaiCodexTicketLifecycleMu.Lock()
	defer s.openaiCodexTicketLifecycleMu.Unlock()
	if s.openaiCodexTicketStopped || s.openaiCodexTicketDone != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	s.openaiCodexTicketCancel = cancel
	s.openaiCodexTicketDone = done
	go func() {
		defer close(done)
		s.openAICodexTicketHarvestLoop(ctx)
	}()
	logger.L().Info("openai_codex_ticket harvester started",
		zap.Int("ttl_seconds", s.openAICodexTicketConfig().TTLSeconds),
		zap.Int("target_length", s.openAICodexTicketConfig().TargetLength),
		zap.Strings("models", s.openAICodexTicketConfig().Models),
	)
}

func (s *OpenAIGatewayService) StopOpenAICodexTicketHarvester() {
	if s == nil {
		return
	}
	s.openaiCodexTicketLifecycleMu.Lock()
	s.openaiCodexTicketStopped = true
	cancel, done := s.openaiCodexTicketCancel, s.openaiCodexTicketDone
	s.openaiCodexTicketLifecycleMu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
}

// Bound admin-triggered rounds without delaying a wake until the normal probe
// interval. This does not change per-account cooldowns or probe limits.
const openAICodexTicketWakeMinInterval = time.Second

func (s *OpenAIGatewayService) openAICodexTicketHarvestLoop(ctx context.Context) {
	timer := time.NewTimer(0)
	defer timer.Stop()
	wake := s.settingService.codexHarvestWakeups()
	var lastRound time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-wake:
			// Execute all work on this single loop. A buffered wake received
			// during a probe is handled after that round, never concurrently.
			delay := time.Until(lastRound.Add(openAICodexTicketWakeMinInterval))
			if delay < 0 {
				delay = 0
			}
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(delay)
		case <-timer.C:
			s.refreshOpenAICodexTickets(ctx)
			lastRound = time.Now()
			timer.Reset(time.Duration(s.openAICodexTicketConfig().HarvestProbeIntervalSeconds) * time.Second)
		}
	}
}

// refreshOpenAICodexTickets probes each account/model with a missing or soon-to-expire
// ticket once. The loop waits for all probes, then waits the configured interval
// before starting the next cycle.
func (s *OpenAIGatewayService) refreshOpenAICodexTickets(ctx context.Context) {
	if s == nil || s.accountRepo == nil || ctx.Err() != nil || !s.openAICodexTicketEnabledContext(ctx) {
		return
	}
	observeCodexHarvestSidecar(ctx)
	accounts, err := s.accountRepo.ListByPlatform(ctx, PlatformOpenAI)
	if err != nil {
		logger.L().Warn("openai_codex_ticket list accounts failed", zap.Error(err))
		return
	}
	cfg := s.openAICodexTicketConfig()
	now := time.Now()
	refreshBefore := time.Duration(cfg.RefreshBeforeSeconds) * time.Second
	if s.settingService.GetCodexTicketStrategy(ctx) == "fixed" {
		refreshBefore = 0
	}
	scope, err := s.settingService.GetCodexTicketHarvestScope(ctx)
	if err != nil {
		logger.L().Warn("openai_codex_ticket harvest scope unavailable; skipping round", zap.Error(err))
		return
	}
	// Independent circular queues prevent a cursor in the deferred tail from
	// bypassing schedulable accounts at the start of the next round.
	tiers := map[codexHarvestTier][]Account{}
	seen := make(map[int64]bool, len(accounts))
	for _, account := range accounts {
		if seen[account.ID] || openAICodexSkipHarvest(&account) || !scope.includes(&account) || !scope.allowsAccount(&account) || !isOpenAICodexTicketAccount(&account) {
			continue
		}
		seen[account.ID] = true
		tier := scope.tier(&account)
		tiers[tier] = append(tiers[tier], account)
	}
	keys := make([]codexHarvestTier, 0, len(tiers))
	for key := range tiers {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].Schedulable != keys[j].Schedulable {
			return keys[i].Schedulable
		}
		if keys[i].Priority != keys[j].Priority {
			return keys[i].Priority < keys[j].Priority
		}
		return keys[i].AccountPriority < keys[j].AccountPriority
	})
	// Remove obsolete buckets when membership/priorities change.
	s.openaiCodexTicketCursors.Range(func(key, _ any) bool {
		tier, ok := key.(codexHarvestTier)
		if !ok {
			s.openaiCodexTicketCursors.Delete(key)
			return true
		}
		if _, exists := tiers[tier]; !exists {
			s.openaiCodexTicketCursors.Delete(key)
		}
		return true
	})
	counts := [2]int{}
	probed := 0
	for _, tier := range keys {
		pool := tiers[tier]
		// The repository orders by global priority only. Stabilize equal-priority
		// rows so database tie ordering cannot defeat round-robin fairness.
		sort.Slice(pool, func(i, j int) bool { return pool[i].ID < pool[j].ID })
		if ctx.Err() != nil || probed >= cfg.MaxProbesPerRound {
			break
		}
		total := len(pool) * len(cfg.Models)
		if total == 0 {
			continue
		}
		stored, _ := s.openaiCodexTicketCursors.LoadOrStore(tier, &atomic.Uint64{})
		cursor, ok := stored.(*atomic.Uint64)
		if !ok || cursor == nil {
			cursor = &atomic.Uint64{}
			s.openaiCodexTicketCursors.Store(tier, cursor)
		}
		start := int(cursor.Load() % uint64(total))
		var wg sync.WaitGroup
		for offset := 0; offset < total && probed < cfg.MaxProbesPerRound && ctx.Err() == nil; offset++ {
			index := (start + offset) % total
			cursor.Store(uint64((index + 1) % total))
			account := pool[index/len(cfg.Models)]
			model := normalizeOpenAICodexTicketModel(cfg.Models[index%len(cfg.Models)])
			if model == "" || s.ticketProbeCoolingDown(account.ID, model, now) {
				continue
			}
			ticket := s.lookupOpenAICodexTicket(&account, model)
			if ticket.valid(now, openAICodexTicketTargetLength(&account, cfg)) && !ticket.needsRefresh(now, refreshBefore) {
				continue
			}
			if ticket != nil && ticket.Standby.valid(now, openAICodexTicketTargetLength(&account, cfg)) && !ticket.Standby.needsRefresh(now, refreshBefore) {
				continue
			}
			acc := account
			acc.Extra = maps.Clone(account.Extra)
			acc.Credentials = maps.Clone(account.Credentials)
			probed++
			if tier.Schedulable {
				counts[0]++
			} else {
				counts[1]++
			}
			wg.Add(1)
			go func(acc Account, model string) {
				defer wg.Done()
				s.probeOnceOpenAICodexTicket(ctx, &acc, model)
			}(acc, model)
		}
		// Complete the high-priority tier before competing for proxy leases.
		wg.Wait()
	}
	if probed > 0 {
		logger.L().Info("openai_codex_ticket probe cycle", zap.Int("probed", probed),
			zap.Int("schedulable_probed", counts[0]), zap.Int("deferred_probed", counts[1]),
			zap.String("scope", scope.Mode), zap.Int("selected_groups", len(scope.GroupIDs)))
	}
}

// probeOnceOpenAICodexTicket 走打票代理打一发。命中合格 292（HTTP 200、长度==target、
// gAAAAA 前缀）就落库；否则记 Info miss，交给下个周期重试。同一 key 并发去重，避免上一发还没
// 回来又叠一发。
func (s *OpenAIGatewayService) probeOnceOpenAICodexTicket(ctx context.Context, account *Account, model string) {
	if account != nil && account.IsRateLimited() {
		return
	}
	if s == nil || !isOpenAICodexTicketAccount(account) || ctx.Err() != nil || !s.openAICodexTicketEnabledContext(ctx) {
		return
	}
	cfg := s.openAICodexTicketConfig()
	proxyURL := s.openAICodexTicketHarvestProxyURLContext(ctx)
	if proxyURL == "" || s.httpUpstream == nil || ctx.Err() != nil {
		return
	}
	if s.ticketProbeCoolingDown(account.ID, model, time.Now()) {
		return
	}
	key := openAICodexTicketKey(account.ID, model)
	_, _, _ = s.openaiCodexTicketFlight.Do(key, func() (any, error) {
		result, httpStatus := "token_error", 0
		length, blocks := 0, 0
		expectedLength := openAICodexTicketTargetLength(account, cfg)
		expectedBlocks := openAICodexTicketExpectedBlocks(account)
		stopWatch := watchCodexHarvestExit()
		defer func() {
			node := stopWatch()
			s.recordCodexProbe(ctx, account, model, result, httpStatus)
			recordCodexHarvestProbe(account, model, result, node, "", httpStatus, length, blocks, expectedLength, expectedBlocks)
		}()
		token, _, err := s.GetAccessToken(ctx, account)
		if err != nil || strings.TrimSpace(token) == "" {
			s.cooldownTicketProbe(account.ID, model, cfg)
			logger.L().Info("openai_codex_ticket probe miss",
				zap.Int64("account_id", account.ID), zap.String("model", model),
				zap.String("reason", "token"), zap.Error(err))
			return nil, nil
		}
		state, status, perr := s.fireOpenAICodexTicketProbe(ctx, account, token, model, proxyURL, time.Duration(cfg.HarvestAttemptTimeoutSeconds)*time.Second)
		httpStatus = status
		length = len(state)
		result = "response_incomplete_or_error"
		if perr != nil {
			s.cooldownTicketProbe(account.ID, model, cfg)
			logger.L().Info("openai_codex_ticket probe miss",
				zap.Int64("account_id", account.ID), zap.String("model", model),
				zap.String("reason", "error"), zap.Error(perr))
			return nil, nil
		}
		shape, shapeErr := parseOpenAICodexTicketShape(state)
		blocks = shape.Blocks
		result = "invalid_state"
		if shapeErr == nil && (shape.IssuedAt.After(time.Now().Add(30*time.Second)) || !time.Now().Before(shape.IssuedAt.Add(time.Hour-30*time.Second))) {
			shapeErr = errors.New("candidate state expired or issued in the future")
		}
		if status != http.StatusOK || shapeErr != nil || shape.Blocks != expectedBlocks || len(state) != expectedLength || !strings.HasPrefix(state, openAICodexTicketStatePrefix) {
			s.cooldownTicketProbe(account.ID, model, cfg)
			logger.L().Info("openai_codex_ticket probe miss",
				zap.Int64("account_id", account.ID), zap.String("model", model),
				zap.Int("http", status), zap.Int("len", len(state)), zap.Int("blocks", shape.Blocks), zap.Error(shapeErr))
			return nil, nil
		}
		s.openaiCodexTicketProbeCooldown.Delete(openAICodexTicketKey(account.ID, model))
		now := time.Now()
		ticket := &openAICodexTicket{
			AccountID:  account.ID,
			Model:      model,
			State:      state,
			Length:     len(state),
			CapturedAt: now,
			ExpiresAt:  now.Add(time.Duration(cfg.TTLSeconds) * time.Second),
			Attempts:   1,
			Blocks:     shape.Blocks,
			IssuedAt:   shape.IssuedAt,
		}
		if expires := shape.IssuedAt.Add(time.Hour - 30*time.Second); expires.Before(ticket.ExpiresAt) {
			ticket.ExpiresAt = expires
		}
		s.storeOpenAICodexTicket(ctx, account, ticket)
		result = "success"
		logger.L().Info("openai_codex_ticket harvested",
			zap.Int64("account_id", account.ID), zap.String("model", model),
			zap.Int("length", ticket.Length), zap.String("mode", "continuous"))
		return nil, nil
	})
}

// IsOpenAICodexTicketExtraKey identifies server-managed ticket material.
func IsOpenAICodexTicketExtraKey(key string) bool {
	return strings.HasPrefix(key, openAICodexTicketExtraKeyPrefix)
}

// MergeOpenAICodexTicketExtra preserves only persisted tickets, never summaries or
// blobs supplied by an account edit. The repository repeats this under the row
// lock so a concurrent harvest cannot be overwritten by a stale admin snapshot.
func MergeOpenAICodexTicketExtra(extra, current map[string]any) map[string]any {
	result := maps.Clone(extra)
	for key := range result {
		if IsOpenAICodexTicketExtraKey(key) {
			delete(result, key)
		}
	}
	for key, value := range current {
		if IsOpenAICodexTicketExtraKey(key) {
			if result == nil {
				result = make(map[string]any)
			}
			result[key] = value
		}
	}
	return result
}

// ValidateOpenAICodexTicketHarvestProxyURL validates only syntax, without making
// a network request or including credentials in validation errors.
func ValidateOpenAICodexTicketHarvestProxyURL(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Hostname() == "" || parsed.Opaque != "" || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return errors.New("harvest proxy must be an HTTP(S) or SOCKS5(h) URL with a host and no path, query or fragment")
	}
	switch parsed.Scheme {
	case "http", "https", "socks5", "socks5h":
	default:
		return errors.New("harvest proxy scheme must be http, https, socks5 or socks5h")
	}
	if port := parsed.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return errors.New("harvest proxy port must be between 1 and 65535")
		}
	}
	return nil
}

// MaskProxyURL never returns a stored proxy password, even for invalid legacy data.
func MaskProxyURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || ValidateOpenAICodexTicketHarvestProxyURL(raw) != nil {
		return ""
	}
	parsed, _ := url.Parse(raw)
	if parsed.User != nil {
		if _, ok := parsed.User.Password(); ok {
			parsed.User = url.UserPassword(parsed.User.Username(), "***")
		}
	}
	return parsed.String()
}

// IsMaskedProxyURL recognizes the exact password placeholder emitted by the API.
func IsMaskedProxyURL(raw string) bool {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return true
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.User == nil {
		return false
	}
	password, ok := parsed.User.Password()
	return ok && password == "***"
}

// Credential shadows do not own tickets. Keep their existing forwarding policy
// instead of imposing a gate for a key the harvester never populates.
func isOpenAICodexTicketAccount(account *Account) bool {
	return account != nil && account.IsOpenAIOAuthLike() && !account.IsShadow()
}

// IsOpenAICodexTicketPrivateExtraKey also covers the retired account-level proxy
// override, whose credentials may remain in older account records.
func IsOpenAICodexTicketPrivateExtraKey(key string) bool {
	return IsOpenAICodexTicketExtraKey(key) || key == "codex_harvest_proxy_url"
}

// RedactOpenAICodexTicketExtra strips ephemeral ticket material from exports
// without changing the source account or unrelated backup fields.
func RedactOpenAICodexTicketExtra(extra map[string]any) map[string]any {
	redacted := maps.Clone(extra)
	for key := range redacted {
		if IsOpenAICodexTicketPrivateExtraKey(key) {
			delete(redacted, key)
		}
	}
	return redacted
}
