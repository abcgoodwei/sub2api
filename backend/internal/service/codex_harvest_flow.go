package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/mihomo"

	"gopkg.in/yaml.v3"
)

const (
	codexHarvestFlowCap            = 200
	codexHarvestFlowSkipDebounce   = 8 * time.Second
	codexHarvestSidecarQueryBudget = 400 * time.Millisecond
	codexHarvestSidecarConfigTTL   = 30 * time.Second
	codexHarvestSidecarGroup       = "CODEX-ROTATE"
)

// CodexHarvestFlowEvent is a redacted breadcrumb for the admin harvest pipeline.
type CodexHarvestFlowEvent struct {
	ID             string    `json:"id"`
	At             time.Time `json:"at"`
	Stage          string    `json:"stage"`
	Kind           string    `json:"kind"`
	AccountID      int64     `json:"account_id,omitempty"`
	AccountName    string    `json:"account_name,omitempty"`
	Model          string    `json:"model,omitempty"`
	Node           string    `json:"node,omitempty"`
	HTTPStatus     int       `json:"http_status,omitempty"`
	Length         int       `json:"length,omitempty"`
	Blocks         int       `json:"blocks,omitempty"`
	ExpectedLength int       `json:"expected_length,omitempty"`
	ExpectedBlocks int       `json:"expected_blocks,omitempty"`
	Accepted       bool      `json:"accepted,omitempty"`
	Standby        bool      `json:"standby,omitempty"`
	Result         string    `json:"result,omitempty"`
	Reason         string    `json:"reason,omitempty"`
	Detail         string    `json:"detail,omitempty"`
}

type CodexHarvestFlowStage struct {
	ID             string     `json:"id"`
	Status         string     `json:"status"`
	Detail         string     `json:"detail,omitempty"`
	At             *time.Time `json:"at,omitempty"`
	Node           string     `json:"node,omitempty"`
	Model          string     `json:"model,omitempty"`
	HTTPStatus     int        `json:"http_status,omitempty"`
	Length         int        `json:"length,omitempty"`
	Blocks         int        `json:"blocks,omitempty"`
	ExpectedLength int        `json:"expected_length,omitempty"`
	ExpectedBlocks int        `json:"expected_blocks,omitempty"`
}

type CodexHarvestFlowAccount struct {
	ID           int64                     `json:"id"`
	Name         string                    `json:"name"`
	Status       string                    `json:"status"`
	Schedulable  bool                      `json:"schedulable"`
	SkipHarvest  bool                      `json:"skip_harvest"`
	InScope      bool                      `json:"in_scope"`
	Tickets      []OpenAICodexTicketStatus `json:"tickets"`
	ReadyCount   int                       `json:"ready_count"`
	BlockedCount int                       `json:"blocked_count"`

	// Availability 当前可用性（口径见 ResolveCodexAccountAvailability，与调度 SQL 一致）。
	// 取值：available / rate_limited / overload / temp_unschedulable / error / disabled / expired
	Availability string `json:"availability"`
	// RecoverAt 被时间窗挡住时预计恢复时间；可用或永久性不可用时为 nil。
	RecoverAt *time.Time `json:"recover_at,omitempty"`
	// TempUnschedulableReason 临时不可调度的原因原文（仅排查展示用）。
	TempUnschedulableReason string `json:"temp_unschedulable_reason,omitempty"`
}

type CodexHarvestFlowHarvest struct {
	Enabled           bool     `json:"enabled"`
	FailClosed        bool     `json:"fail_closed"`
	Strategy          string   `json:"strategy"`
	ScopeMode         string   `json:"scope_mode,omitempty"`
	AccountPolicy     string   `json:"account_policy,omitempty"`
	GroupIDs          []int64  `json:"group_ids,omitempty"`
	Models            []string `json:"models"`
	TargetLength      int      `json:"target_length"`
	ProbeIntervalSec  int      `json:"probe_interval_seconds"`
	CooldownSec       int      `json:"cooldown_seconds"`
	MaxProbesPerRound int      `json:"max_probes_per_round"`
	HarvestProxy      string   `json:"harvest_proxy,omitempty"`
	// 以下两项此前未回传，导致前端无法回读、只能硬编码默认值（永远显示 25/600）。
	AttemptTimeoutSec int `json:"attempt_timeout_seconds"`
	RefreshBeforeSec  int `json:"refresh_before_seconds"`
}

type CodexHarvestFlowSidecar struct {
	Reachable  bool       `json:"reachable"`
	Source     string     `json:"source,omitempty"`
	Controller string     `json:"controller,omitempty"`
	Group      string     `json:"group,omitempty"`
	Type       string     `json:"type,omitempty"`
	Now        string     `json:"now,omitempty"`
	AllCount   int        `json:"all_count,omitempty"`
	Error      string     `json:"error,omitempty"`
	ObservedAt *time.Time `json:"observed_at,omitempty"`
}

type CodexHarvestFlowCounts struct {
	ProbeHit       int `json:"probe_hit"`
	ProbeMiss      int `json:"probe_miss"`
	TicketAccept   int `json:"ticket_accept"`
	TicketReject   int `json:"ticket_reject"`
	SelectOK       int `json:"select_ok"`
	SelectSkip     int `json:"select_skip"`
	SelectFail     int `json:"select_fail"`
	TicketsReady   int `json:"tickets_ready"`
	TicketsBlocked int `json:"tickets_blocked"`
}

type CodexHarvestFlowSnapshot struct {
	GeneratedAt time.Time                 `json:"generated_at"`
	Harvest     CodexHarvestFlowHarvest   `json:"harvest"`
	Sidecar     CodexHarvestFlowSidecar   `json:"sidecar"`
	Stages      []CodexHarvestFlowStage   `json:"stages"`
	Accounts    []CodexHarvestFlowAccount `json:"accounts"`
	Counts      CodexHarvestFlowCounts    `json:"counts"`
	Events      []CodexHarvestFlowEvent   `json:"events"`
}

type codexHarvestFlowRing struct {
	mu       sync.Mutex
	seq      atomic.Uint64
	events   []CodexHarvestFlowEvent
	skips    map[string]time.Time
	lastNow  string
	lastType string
	lastAll  int
}

var defaultCodexHarvestFlow = &codexHarvestFlowRing{skips: make(map[string]time.Time)}

type cachedSidecarController struct {
	until      time.Time
	controller string
	secret     string
	source     string
	err        string
}

var sidecarControllerCache atomic.Pointer[cachedSidecarController]

func resetCodexHarvestFlow() {
	defaultCodexHarvestFlow.mu.Lock()
	defer defaultCodexHarvestFlow.mu.Unlock()
	defaultCodexHarvestFlow.events = nil
	defaultCodexHarvestFlow.skips = make(map[string]time.Time)
	defaultCodexHarvestFlow.lastNow = ""
	defaultCodexHarvestFlow.lastType = ""
	defaultCodexHarvestFlow.lastAll = 0
	defaultCodexHarvestFlow.seq.Store(0)
}

func recordCodexHarvestFlow(event CodexHarvestFlowEvent) {
	if event.At.IsZero() {
		event.At = time.Now()
	}
	event.AccountName = clipFlowText(event.AccountName, 80)
	event.Model = clipFlowText(event.Model, 64)
	event.Node = clipFlowText(event.Node, 160)
	event.Result = clipFlowText(event.Result, 64)
	event.Reason = clipFlowText(event.Reason, 64)
	event.Detail = clipFlowText(event.Detail, 240)
	defaultCodexHarvestFlow.mu.Lock()
	defer defaultCodexHarvestFlow.mu.Unlock()
	if event.ID == "" {
		event.ID = fmt.Sprintf("%d-%d", event.At.UnixNano(), defaultCodexHarvestFlow.seq.Add(1))
	}
	defaultCodexHarvestFlow.events = append(defaultCodexHarvestFlow.events, event)
	if overflow := len(defaultCodexHarvestFlow.events) - codexHarvestFlowCap; overflow > 0 {
		defaultCodexHarvestFlow.events = append([]CodexHarvestFlowEvent(nil), defaultCodexHarvestFlow.events[overflow:]...)
	}
}

func listCodexHarvestFlowEvents() []CodexHarvestFlowEvent {
	defaultCodexHarvestFlow.mu.Lock()
	defer defaultCodexHarvestFlow.mu.Unlock()
	out := make([]CodexHarvestFlowEvent, len(defaultCodexHarvestFlow.events))
	copy(out, defaultCodexHarvestFlow.events)
	return out
}

func recordCodexHarvestNode(now, groupType string, allCount int) {
	now = strings.TrimSpace(now)
	if now == "" {
		return
	}
	defaultCodexHarvestFlow.mu.Lock()
	if allCount > 0 {
		defaultCodexHarvestFlow.lastAll = allCount
	} else {
		allCount = defaultCodexHarvestFlow.lastAll
	}
	if groupType != "" {
		defaultCodexHarvestFlow.lastType = groupType
	} else {
		groupType = defaultCodexHarvestFlow.lastType
	}
	changed := defaultCodexHarvestFlow.lastNow != now
	defaultCodexHarvestFlow.lastNow = now
	defaultCodexHarvestFlow.mu.Unlock()
	if !changed {
		return
	}
	recordCodexHarvestFlow(CodexHarvestFlowEvent{
		Stage:  "node",
		Kind:   "rotate",
		Node:   now,
		Result: groupType,
		Detail: fmt.Sprintf("pool=%d", allCount),
	})
}

func recordCodexHarvestProbe(account *Account, model, result, node, detail string, httpStatus, length, blocks, expectedLength, expectedBlocks int) {
	kind := "probe_miss"
	if result == "success" {
		kind = "probe_hit"
	}
	event := CodexHarvestFlowEvent{
		Stage:          "probe",
		Kind:           kind,
		Model:          model,
		Node:           node,
		HTTPStatus:     httpStatus,
		Length:         length,
		Blocks:         blocks,
		ExpectedLength: expectedLength,
		ExpectedBlocks: expectedBlocks,
		Accepted:       result == "success",
		Result:         result,
		Detail:         detail,
	}
	if account != nil {
		event.AccountID = account.ID
		event.AccountName = account.Name
	}
	recordCodexHarvestFlow(event)
}

func recordCodexHarvestTicketStore(account *Account, ticket *openAICodexTicket, standby bool) {
	if ticket == nil {
		return
	}
	event := CodexHarvestFlowEvent{
		Stage:    "ticket",
		Kind:     "accept",
		Model:    ticket.Model,
		Length:   ticket.Length,
		Blocks:   ticket.Blocks,
		Accepted: true,
		Standby:  standby,
	}
	if account != nil {
		event.AccountID = account.ID
		event.AccountName = account.Name
		event.ExpectedLength = openAICodexTicketExpectedLength(account)
		event.ExpectedBlocks = openAICodexTicketExpectedBlocks(account)
	}
	recordCodexHarvestFlow(event)
}

func recordCodexHarvestTicketReject(account *Account, model, detail string, length, blocks int) {
	event := CodexHarvestFlowEvent{
		Stage:  "ticket",
		Kind:   "reject",
		Model:  model,
		Length: length,
		Blocks: blocks,
		Result: "response_mismatch",
		Detail: detail,
	}
	if account != nil {
		event.AccountID = account.ID
		event.AccountName = account.Name
		event.ExpectedLength = openAICodexTicketExpectedLength(account)
		event.ExpectedBlocks = openAICodexTicketExpectedBlocks(account)
	}
	recordCodexHarvestFlow(event)
}

func recordCodexHarvestSelect(account *Account, model, kind, reason, detail string) {
	if kind == "skip" || kind == "failed" {
		key := fmt.Sprintf("%d\x00%s", 0, model)
		if account != nil {
			key = openAICodexTicketKey(account.ID, model)
		}
		now := time.Now()
		defaultCodexHarvestFlow.mu.Lock()
		if until, ok := defaultCodexHarvestFlow.skips[key]; ok && now.Before(until) {
			defaultCodexHarvestFlow.mu.Unlock()
			return
		}
		defaultCodexHarvestFlow.skips[key] = now.Add(codexHarvestFlowSkipDebounce)
		defaultCodexHarvestFlow.mu.Unlock()
	}
	event := CodexHarvestFlowEvent{
		Stage:  "select",
		Kind:   kind,
		Model:  model,
		Reason: reason,
		Detail: detail,
	}
	if account != nil {
		event.AccountID = account.ID
		event.AccountName = account.Name
	}
	recordCodexHarvestFlow(event)
}

func clipFlowText(value string, max int) string {
	value = strings.TrimSpace(value)
	if max <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= max {
		return value
	}
	return string(runes[:max])
}

func watchCodexHarvestExit() func() string {
	done := make(chan struct{})
	var latest atomic.Value
	latest.Store("")
	go func() {
		ticker := time.NewTicker(80 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				if node := peekCodexHarvestExit(); node != "" {
					latest.Store(node)
				}
			}
		}
	}()
	return func() string {
		close(done)
		node, _ := latest.Load().(string)
		if node == "" {
			node = peekCodexHarvestExit()
		}
		if node == "" {
			node = lastCodexHarvestNode()
		}
		return node
	}
}

func peekCodexHarvestExit() string {
	ctrl := loadCodexHarvestSidecarController()
	if ctrl.controller == "" {
		return lastCodexHarvestNode()
	}
	queryCtx, cancel := context.WithTimeout(context.Background(), codexHarvestSidecarQueryBudget)
	defer cancel()
	node, err := queryCodexRotateExit(queryCtx, ctrl.controller, ctrl.secret)
	if err != nil || node == "" {
		return lastCodexHarvestNode()
	}
	recordCodexHarvestNode(node, "", 0)
	return node
}

func observeCodexHarvestSidecar(ctx context.Context) CodexHarvestFlowSidecar {
	ctrl := loadCodexHarvestSidecarController()
	out := CodexHarvestFlowSidecar{Group: codexHarvestSidecarGroup, Source: ctrl.source, Controller: redactController(ctrl.controller)}
	if ctrl.err != "" && ctrl.controller == "" {
		out.Error = ctrl.err
		return out
	}
	if ctrl.controller == "" {
		out.Error = "sidecar controller not found"
		return out
	}
	queryCtx, cancel := context.WithTimeout(ctx, codexHarvestSidecarQueryBudget)
	defer cancel()
	group, err := queryCodexRotateGroup(queryCtx, ctrl.controller, ctrl.secret)
	now := time.Now()
	out.ObservedAt = &now
	if err != nil {
		out.Error = clipFlowText(err.Error(), 160)
		return out
	}
	out.Reachable = true
	out.Type = group.Type
	out.Now = strings.TrimSpace(group.Now)
	out.AllCount = len(group.All)
	if out.Now == "" {
		exitCtx, exitCancel := context.WithTimeout(ctx, codexHarvestSidecarQueryBudget)
		node, err := queryCodexRotateExit(exitCtx, ctrl.controller, ctrl.secret)
		exitCancel()
		if err == nil {
			out.Now = node
		}
	}
	if out.Now == "" {
		out.Now = lastCodexHarvestNode()
	}
	recordCodexHarvestNode(out.Now, group.Type, len(group.All))
	return out
}

func redactController(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	return strings.TrimPrefix(strings.TrimPrefix(raw, "https://"), "http://")
}

func loadCodexHarvestSidecarController() cachedSidecarController {
	if controller, secret, ok := mihomo.ManagedController(); ok {
		return cachedSidecarController{controller: controller, secret: secret, source: "managed"}
	}

	if cached := sidecarControllerCache.Load(); cached != nil && time.Now().Before(cached.until) {
		return *cached
	}
	next := cachedSidecarController{until: time.Now().Add(codexHarvestSidecarConfigTTL)}
	dataDir := strings.TrimSpace(os.Getenv("DATA_DIR"))
	if dataDir == "" {
		dataDir = "."
	}
	// Web-managed Mihomo runs candidate.json; config.yaml remains the legacy
	// sidecar format. yaml.Unmarshal also accepts the managed JSON document.
	root, err := os.OpenRoot(dataDir)
	if err != nil {
		next.err = "sidecar config missing"
		sidecarControllerCache.Store(&next)
		return next
	}
	defer func() { _ = root.Close() }()
	raw, err := root.ReadFile("mihomo-codex/candidate.json")
	if os.IsNotExist(err) {
		raw, err = root.ReadFile("mihomo-codex/config.yaml")
	}
	if err != nil {
		next.err = "sidecar config missing"
		sidecarControllerCache.Store(&next)
		return next
	}
	var parsed struct {
		ExternalController string `yaml:"external-controller"`
		Secret             string `yaml:"secret"`
	}
	if yaml.Unmarshal(raw, &parsed) != nil {
		next.err = "sidecar config invalid"
		sidecarControllerCache.Store(&next)
		return next
	}
	controller := strings.TrimSpace(parsed.ExternalController)
	if controller == "" {
		controller = "127.0.0.1:9098"
	}
	if !strings.Contains(controller, "://") {
		controller = "http://" + controller
	}
	if !loopbackHTTPURL(controller) {
		next.err = "sidecar controller is not loopback"
		sidecarControllerCache.Store(&next)
		return next
	}
	next.controller = controller
	next.secret = strings.TrimSpace(parsed.Secret)
	next.source = "sidecar"
	sidecarControllerCache.Store(&next)
	return next
}

func loopbackHTTPURL(raw string) bool {
	if !strings.HasPrefix(raw, "http://") && !strings.HasPrefix(raw, "https://") {
		return false
	}
	host := strings.TrimPrefix(strings.TrimPrefix(raw, "https://"), "http://")
	if slash := strings.IndexByte(host, '/'); slash >= 0 {
		host = host[:slash]
	}
	hostname, _, err := net.SplitHostPort(host)
	if err != nil {
		hostname = host
	}
	if strings.EqualFold(hostname, "localhost") {
		return true
	}
	ip := net.ParseIP(hostname)
	return ip != nil && ip.IsLoopback()
}

type clashProxyGroup struct {
	Name string   `json:"name"`
	Type string   `json:"type"`
	Now  string   `json:"now"`
	All  []string `json:"all"`
}

func queryCodexRotateGroup(ctx context.Context, controller, secret string) (clashProxyGroup, error) {
	body, err := clashGET(ctx, controller, secret, "/proxies/"+codexHarvestSidecarGroup, 1<<20)
	if err != nil {
		return clashProxyGroup{}, err
	}
	var group clashProxyGroup
	if json.Unmarshal(body, &group) != nil {
		return clashProxyGroup{}, fmt.Errorf("controller payload invalid")
	}
	return group, nil
}

func queryCodexRotateExit(ctx context.Context, controller, secret string) (string, error) {
	body, err := clashGET(ctx, controller, secret, "/connections", 2<<20)
	if err != nil {
		return "", err
	}
	return exitNodeFromConnections(body), nil
}

func clashGET(ctx context.Context, controller, secret, path string, limit int64) ([]byte, error) {
	if limit <= 0 {
		limit = 1 << 20
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(controller, "/")+path, nil)
	if err != nil {
		return nil, err
	}
	if secret != "" {
		req.Header.Set("Authorization", "Bearer "+secret)
	}
	client := &http.Client{Timeout: codexHarvestSidecarQueryBudget, Transport: &http.Transport{Proxy: nil}}
	defer client.CloseIdleConnections()
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("controller unavailable")
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit))
	if err != nil {
		return nil, fmt.Errorf("controller read failed")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("controller http %d", resp.StatusCode)
	}
	return body, nil
}

func exitNodeFromConnections(body []byte) string {
	var payload struct {
		Connections []struct {
			Chains []string `json:"chains"`
			Start  string   `json:"start"`
		} `json:"connections"`
	}
	if json.Unmarshal(body, &payload) != nil {
		return ""
	}
	best, bestStart := "", ""
	for _, conn := range payload.Connections {
		node := nodeFromChains(conn.Chains)
		if node == "" {
			continue
		}
		if best == "" || conn.Start >= bestStart {
			best = node
			bestStart = conn.Start
		}
	}
	return best
}

func nodeFromChains(chains []string) string {
	for _, hop := range chains {
		hop = strings.TrimSpace(hop)
		if hop != "" && hop != codexHarvestSidecarGroup {
			return hop
		}
	}
	return ""
}

func BuildCodexHarvestFlow(ctx context.Context, cfg *config.Config, settings *SettingService, accounts []Account) CodexHarvestFlowSnapshot {
	now := time.Now()
	ticketCfg := config.OpenAICodexTicketConfig{}
	if cfg != nil {
		ticketCfg = cfg.Gateway.OpenAICodexTicket
	}
	if ticketCfg.TargetLength <= 0 {
		ticketCfg.TargetLength = 292
	}
	if ticketCfg.HarvestProbeIntervalSeconds < 30 {
		ticketCfg.HarvestProbeIntervalSeconds = 180
	}
	if ticketCfg.HarvestCooldownSeconds <= 0 {
		ticketCfg.HarvestCooldownSeconds = 180
	}
	if ticketCfg.MaxProbesPerRound <= 0 {
		ticketCfg.MaxProbesPerRound = 6
	}
	if len(ticketCfg.Models) == 0 {
		ticketCfg.Models = []string{openAICodexTicketDefaultModel, openAICodexTicketDefaultSolModel}
	}
	enabled := ticketCfg.Enabled
	failClosed := ticketCfg.FailClosed
	harvestProxy := strings.TrimSpace(ticketCfg.HarvestProxyURL)
	strategy := "standby"
	scope := CodexTicketHarvestScope{Mode: "all", AccountPolicy: CodexHarvestSchedulableOnly}
	if settings != nil {
		enabled = settings.GetOpenAICodexTicketEnabled(ctx, enabled)
		failClosed = settings.GetOpenAICodexTicketFailClosed(ctx)
		ticketCfg.Models = settings.GetOpenAICodexTicketModels(ctx, ticketCfg.Models)
		ticketCfg.HarvestProbeIntervalSeconds = settings.GetOpenAICodexTicketProbeIntervalSeconds(ctx, ticketCfg.HarvestProbeIntervalSeconds)
		ticketCfg.HarvestCooldownSeconds = settings.GetOpenAICodexTicketCooldownSeconds(ctx, ticketCfg.HarvestCooldownSeconds)
		ticketCfg.MaxProbesPerRound = settings.GetOpenAICodexTicketMaxProbesPerRound(ctx, ticketCfg.MaxProbesPerRound)
		ticketCfg.HarvestAttemptTimeoutSeconds = settings.GetOpenAICodexTicketAttemptTimeoutSeconds(ctx, ticketCfg.HarvestAttemptTimeoutSeconds)
		ticketCfg.RefreshBeforeSeconds = settings.GetOpenAICodexTicketRefreshBeforeSeconds(ctx, ticketCfg.RefreshBeforeSeconds)
		if proxy := settings.GetOpenAICodexTicketHarvestProxyURL(ctx); proxy != "" {
			harvestProxy = proxy
		}
		strategy = settings.GetCodexTicketStrategy(ctx)
		if current, err := settings.GetCodexTicketHarvestScope(ctx); err == nil {
			scope = current
		}
	}
	ticketCfg.Enabled = enabled
	ticketCfg.FailClosed = failClosed
	snapshot := CodexHarvestFlowSnapshot{
		GeneratedAt: now,
		Harvest: CodexHarvestFlowHarvest{
			Enabled:           enabled,
			FailClosed:        failClosed,
			Strategy:          strategy,
			ScopeMode:         scope.Mode,
			AccountPolicy:     scope.AccountPolicy,
			GroupIDs:          scope.GroupIDs,
			Models:            ticketCfg.Models,
			TargetLength:      ticketCfg.TargetLength,
			ProbeIntervalSec:  ticketCfg.HarvestProbeIntervalSeconds,
			CooldownSec:       ticketCfg.HarvestCooldownSeconds,
			MaxProbesPerRound: ticketCfg.MaxProbesPerRound,
			HarvestProxy:      MaskProxyURL(harvestProxy),
			AttemptTimeoutSec: ticketCfg.HarvestAttemptTimeoutSeconds,
			RefreshBeforeSec:  ticketCfg.RefreshBeforeSeconds,
		},
		Sidecar: observeCodexHarvestSidecar(ctx),
		Events:  listCodexHarvestFlowEvents(),
	}
	if snapshot.Harvest.HarvestProxy == "" && harvestProxy == mihomo.Endpoint {
		snapshot.Harvest.HarvestProxy = mihomo.Endpoint
	}
	for _, account := range accounts {
		if !isOpenAICodexTicketAccount(&account) {
			continue
		}
		availability, recoverAt := ResolveCodexAccountAvailability(&account, now)
		item := CodexHarvestFlowAccount{
			ID:          account.ID,
			Name:        account.Name,
			Status:      account.Status,
			Schedulable: account.Schedulable,
			SkipHarvest: openAICodexSkipHarvest(&account),
			InScope:     !openAICodexSkipHarvest(&account) && scope.includes(&account),
			Tickets:     OpenAICodexTicketStatuses(&account, ticketCfg, now),

			Availability:            availability,
			RecoverAt:               recoverAt,
			TempUnschedulableReason: account.TempUnschedulableReason,
		}
		for _, ticket := range item.Tickets {
			if ticket.Ready {
				item.ReadyCount++
				snapshot.Counts.TicketsReady++
			}
			if ticket.Blocked {
				item.BlockedCount++
				snapshot.Counts.TicketsBlocked++
			}
		}
		snapshot.Accounts = append(snapshot.Accounts, item)
	}
	for _, event := range snapshot.Events {
		switch event.Kind {
		case "probe_hit":
			snapshot.Counts.ProbeHit++
		case "probe_miss":
			snapshot.Counts.ProbeMiss++
		case "accept":
			snapshot.Counts.TicketAccept++
		case "reject":
			snapshot.Counts.TicketReject++
		case "selected":
			snapshot.Counts.SelectOK++
		case "skip":
			snapshot.Counts.SelectSkip++
		case "failed", "unavailable":
			snapshot.Counts.SelectFail++
		}
	}
	snapshot.Stages = buildCodexHarvestFlowStages(snapshot)
	snapshot.Events = reverseCodexHarvestFlowEvents(snapshot.Events)
	return snapshot
}

func reverseCodexHarvestFlowEvents(events []CodexHarvestFlowEvent) []CodexHarvestFlowEvent {
	if len(events) == 0 {
		return events
	}
	out := make([]CodexHarvestFlowEvent, len(events))
	for i := range events {
		out[len(events)-1-i] = events[i]
	}
	return out
}

func lastCodexHarvestNode() string {
	defaultCodexHarvestFlow.mu.Lock()
	defer defaultCodexHarvestFlow.mu.Unlock()
	return defaultCodexHarvestFlow.lastNow
}

func buildCodexHarvestFlowStages(snapshot CodexHarvestFlowSnapshot) []CodexHarvestFlowStage {
	last := map[string]CodexHarvestFlowEvent{}
	for _, event := range snapshot.Events {
		last[event.Stage+":"+event.Kind] = event
		last[event.Stage] = event
	}
	node := CodexHarvestFlowStage{ID: "node", Status: "idle", Node: snapshot.Sidecar.Now}
	switch {
	case snapshot.Sidecar.Reachable && snapshot.Sidecar.Now != "":
		node.Status = "ok"
		node.Detail = snapshot.Sidecar.Now
		node.At = snapshot.Sidecar.ObservedAt
	case snapshot.Sidecar.Reachable && snapshot.Sidecar.AllCount > 0:
		node.Status = "ok"
		node.At = snapshot.Sidecar.ObservedAt
		kind := snapshot.Sidecar.Type
		if kind == "" {
			kind = "LoadBalance"
		}
		node.Detail = fmt.Sprintf("%s · %d nodes", kind, snapshot.Sidecar.AllCount)
	case snapshot.Sidecar.Error != "":
		node.Status = "fail"
		node.Detail = snapshot.Sidecar.Error
	default:
		node.Detail = "waiting for sidecar"
	}
	if event, ok := last["node"]; ok {
		at := event.At
		node.At = &at
		copyFlowMetrics(&node, event)
		if node.Detail == "" {
			node.Detail = event.Node
		}
	}

	probe := stageFromEvent("probe", last["probe"], "waiting for harvest probe")
	if event, ok := last["probe"]; ok {
		copyFlowMetrics(&probe, event)
		if event.Kind == "probe_hit" {
			probe.Status = "ok"
		} else {
			probe.Status = "warn"
			if event.HTTPStatus >= 500 || event.Result == "token_error" {
				probe.Status = "fail"
			}
		}
	}

	shape := shapeStageFromEvents(last, snapshot.Counts.TicketsReady)

	ticket := CodexHarvestFlowStage{ID: "ticket", Status: "idle", Detail: "no stored ticket"}
	switch {
	case snapshot.Counts.TicketsReady > 0:
		ticket.Status = "ok"
		ticket.Detail = fmt.Sprintf("%d ready", snapshot.Counts.TicketsReady)
	case snapshot.Counts.TicketsBlocked > 0:
		ticket.Status = "fail"
		ticket.Detail = fmt.Sprintf("%d paused", snapshot.Counts.TicketsBlocked)
	}
	if event, ok := last["ticket"]; ok {
		at := event.At
		ticket.At = &at
		ticket.Model = event.Model
		copyFlowMetrics(&ticket, event)
		if event.Kind == "reject" && snapshot.Counts.TicketsReady == 0 {
			ticket.Status = "fail"
			ticket.Detail = "response rejected"
		} else if event.Kind == "accept" && ticket.Status != "fail" {
			ticket.Status = "ok"
			if snapshot.Counts.TicketsReady == 0 && event.Standby {
				ticket.Detail = "standby stored"
			}
		}
	}

	selectStage := stageFromEvent("select", last["select"], "waiting for assistant request")
	if event, ok := last["select"]; ok {
		copyFlowMetrics(&selectStage, event)
		switch event.Kind {
		case "selected":
			selectStage.Status = "ok"
		case "skip":
			selectStage.Status = "warn"
		default:
			selectStage.Status = "fail"
		}
	}
	return []CodexHarvestFlowStage{node, probe, shape, ticket, selectStage}
}

func shapeStageFromEvents(last map[string]CodexHarvestFlowEvent, ready int) CodexHarvestFlowStage {
	shape := CodexHarvestFlowStage{ID: "shape", Status: "idle", Detail: "waiting for 292 check"}
	latest := last["probe"]
	hit := last["probe:probe_hit"]
	if flowEventEmpty(latest) && flowEventEmpty(hit) {
		return shape
	}
	if latest.HTTPStatus == 200 && latest.Length > 0 && (latest.Kind != "probe_hit" || (latest.ExpectedLength > 0 && latest.Length != latest.ExpectedLength)) {
		at := latest.At
		shape.At = &at
		shape.Model = latest.Model
		copyFlowMetrics(&shape, latest)
		shape.Status = "fail"
		shape.Detail = fmt.Sprintf("got %d/%d want %d/%d", latest.Length, latest.Blocks, latest.ExpectedLength, latest.ExpectedBlocks)
		return shape
	}
	source := hit
	if flowEventEmpty(source) {
		source = latest
	}
	if source.Kind == "probe_hit" && (source.ExpectedLength == 0 || source.Length == source.ExpectedLength) {
		at := source.At
		shape.At = &at
		shape.Model = source.Model
		copyFlowMetrics(&shape, source)
		shape.Status = "ok"
		shape.Detail = fmt.Sprintf("%d / %d blk", source.Length, source.Blocks)
		return shape
	}
	if ready > 0 {
		shape.Status = "ok"
		shape.Detail = fmt.Sprintf("%d ready", ready)
		return shape
	}
	if !flowEventEmpty(latest) {
		at := latest.At
		shape.At = &at
		shape.Model = latest.Model
		copyFlowMetrics(&shape, latest)
		shape.Status = "warn"
		shape.Detail = "no ticket body"
	}
	return shape
}

func flowEventEmpty(event CodexHarvestFlowEvent) bool {
	return event.ID == "" && event.At.IsZero()
}

func stageFromEvent(id string, event CodexHarvestFlowEvent, idle string) CodexHarvestFlowStage {
	stage := CodexHarvestFlowStage{ID: id, Status: "idle", Detail: idle}
	if event.ID == "" && event.At.IsZero() {
		return stage
	}
	at := event.At
	stage.At = &at
	stage.Model = event.Model
	copyFlowMetrics(&stage, event)
	if event.Detail != "" {
		stage.Detail = event.Detail
	} else if event.Result != "" {
		stage.Detail = event.Result
	} else if event.Reason != "" {
		stage.Detail = event.Reason
	} else if event.AccountName != "" {
		stage.Detail = event.AccountName
	}
	return stage
}

func copyFlowMetrics(stage *CodexHarvestFlowStage, event CodexHarvestFlowEvent) {
	if stage == nil {
		return
	}
	if event.Node != "" {
		stage.Node = event.Node
	}
	stage.HTTPStatus = event.HTTPStatus
	stage.Length = event.Length
	stage.Blocks = event.Blocks
	stage.ExpectedLength = event.ExpectedLength
	stage.ExpectedBlocks = event.ExpectedBlocks
}
