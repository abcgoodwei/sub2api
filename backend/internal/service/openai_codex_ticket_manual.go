package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"go.uber.org/zap"
)

// ManualHarvestRequest 定义手动打票的配置参数
type ManualHarvestRequest struct {
	AccountID                int64    `json:"account_id"`
	Models                   []string `json:"models"`
	ProbeIntervalSeconds     int      `json:"probe_interval_seconds"`      // 对应检测周期，1~300s，默认 10
	RateLimitCooldownSeconds int      `json:"rate_limit_cooldown_seconds"` // 对应 429 冷静，1~60s，默认 30
	MaxAttempts              int      `json:"max_attempts"`                // 最大尝试次数，1~100，默认 20
	NodeSwitchRule           string   `json:"node_switch_rule"`            // 312_or_2fail | every_request | 312_only | never
	StopOnSuccess            bool     `json:"stop_on_success"`             // 命中合规票后是否自动终止
}

// 手动打票由管理员直接发起，不经过前端表单，因此服务端必须自行做上下界收敛：
// 没有上界时一个超大的 probe_interval_seconds / max_attempts 会让请求在服务端
// 挂很久并持续打上游。范围与前端输入框保持一致。
const (
	manualHarvestProbeIntervalMin     = 1
	manualHarvestProbeIntervalMax     = 300
	manualHarvestRateLimitCooldownMin = 1
	manualHarvestRateLimitCooldownMax = 60
	manualHarvestMaxAttemptsMin       = 1
	manualHarvestMaxAttemptsMax       = 100
	manualHarvestMaxModels            = 20
)

// clampManualHarvestInt 把越界值收敛到 [min,max]，零值/缺省值交给 fallback。
func clampManualHarvestInt(value, fallback, minValue, maxValue int) int {
	if value < minValue {
		if value == 0 {
			return fallback
		}
		return minValue
	}
	if value > maxValue {
		return maxValue
	}
	return value
}

// normalizeManualHarvestRequest 收敛外部传入的参数，任何越界值都会被夹到边界
// 而不是被拒绝，避免管理员因为一个笔误拿不到结果。
func normalizeManualHarvestRequest(req *ManualHarvestRequest) {
	if req == nil {
		return
	}
	req.ProbeIntervalSeconds = clampManualHarvestInt(req.ProbeIntervalSeconds, 10,
		manualHarvestProbeIntervalMin, manualHarvestProbeIntervalMax)
	req.RateLimitCooldownSeconds = clampManualHarvestInt(req.RateLimitCooldownSeconds, 30,
		manualHarvestRateLimitCooldownMin, manualHarvestRateLimitCooldownMax)
	req.MaxAttempts = clampManualHarvestInt(req.MaxAttempts, 20,
		manualHarvestMaxAttemptsMin, manualHarvestMaxAttemptsMax)

	models := make([]string, 0, len(req.Models))
	seen := make(map[string]struct{}, len(req.Models))
	for _, model := range req.Models {
		model = normalizeOpenAICodexTicketModel(model)
		if model == "" {
			continue
		}
		if _, exists := seen[model]; exists {
			continue
		}
		seen[model] = struct{}{}
		models = append(models, model)
		if len(models) >= manualHarvestMaxModels {
			break
		}
	}
	if len(models) == 0 {
		models = []string{openAICodexTicketDefaultModel, openAICodexTicketDefaultSolModel}
	}
	req.Models = models
}

// ManualHarvestProgress 实时反馈每一步的进度与状态
type ManualHarvestProgress struct {
	Attempt     int    `json:"attempt"`
	MaxAttempts int    `json:"max_attempts"`
	Model       string `json:"model"`
	Node        string `json:"node"`
	HTTPStatus  int    `json:"http_status"`
	Length      int    `json:"length"`
	Blocks      int    `json:"blocks"`
	ExpectedLen int    `json:"expected_length"`
	ExpectedBlk int    `json:"expected_blocks"`
	Result      string `json:"result"` // hit | miss_degraded | rate_limited | error | done
	// Level 日志级别：OK / WARN / ERROR，前端据此上色，扫一眼就知道严重程度。
	Level string `json:"level,omitempty"`
	// Message 通俗主文案：说清楚"发生了什么 + 系统会怎么处理"。
	Message string `json:"message"`
	// Detail 原始技术细节（如 invalid probe event），主文案看不懂时用来排障。
	Detail        string `json:"detail,omitempty"`
	TicketsStored int    `json:"tickets_stored"`
	Done          bool   `json:"done"`
}

// ExecuteManualHarvest 执行定向单号打票
func (s *OpenAIGatewayService) ExecuteManualHarvest(
	ctx context.Context,
	req ManualHarvestRequest,
	progressCallback func(p ManualHarvestProgress),
) error {
	if s == nil || s.accountRepo == nil {
		return errors.New("gateway service unavailable")
	}

	account, err := s.accountRepo.GetByID(ctx, req.AccountID)
	if err != nil || account == nil {
		return fmt.Errorf("account %d not found: %w", req.AccountID, err)
	}
	// 定向打票会把该账号的 access token 发往 ChatGPT 打票接口，因此必须限定在
	// 真正支持 Codex 票据的账号上：非 OpenAI 账号、API Key 账号和影子账号都
	// 拿不到合规票据，直接拒绝而不是发一次注定失败的请求。
	if !isOpenAICodexTicketAccount(account) {
		return fmt.Errorf("account %d does not support Codex ticket harvesting", req.AccountID)
	}

	normalizeManualHarvestRequest(&req)

	cfg := s.openAICodexTicketConfig()
	proxyURL := s.openAICodexTicketHarvestProxyURLContext(ctx)
	if proxyURL == "" {
		proxyURL = "http://127.0.0.1:3101"
	}

	expectedBlocks := openAICodexTicketExpectedBlocks(account)
	expectedLength := openAICodexTicketTargetLength(account, cfg)

	consecutiveFails := 0
	ticketsStored := 0

	for attempt := 1; attempt <= req.MaxAttempts; attempt++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		for _, model := range req.Models {
			if ctx.Err() != nil {
				return ctx.Err()
			}

			// 检查换节点规则
			shouldSwitchNode := false
			switch req.NodeSwitchRule {
			case "every_request":
				shouldSwitchNode = true
			case "312_or_2fail":
				if consecutiveFails >= 2 {
					shouldSwitchNode = true
				}
			}

			if shouldSwitchNode {
				logger.L().Info("manual_harvest: node switch triggered", zap.String("rule", req.NodeSwitchRule))
				consecutiveFails = 0
			}

			token, _, err := s.GetAccessToken(ctx, account)
			if err != nil || strings.TrimSpace(token) == "" {
				consecutiveFails++
				progressCallback(ManualHarvestProgress{
					Attempt:     attempt,
					MaxAttempts: req.MaxAttempts,
					Model:       model,
					Result:      "error",
					Level:       "ERROR",
					Message:     "无法获取该账号的登录令牌，本次尝试已跳过",
					Detail:      fmt.Sprintf("get access token failed: %v", err),
				})
				if err := sleepWithContext(ctx, time.Duration(req.ProbeIntervalSeconds)*time.Second); err != nil {
					return err
				}
				continue
			}

			stopWatch := watchCodexHarvestExit()
			state, status, perr := s.fireOpenAICodexTicketProbe(
				ctx, account, token, model, proxyURL,
				time.Duration(cfg.HarvestAttemptTimeoutSeconds)*time.Second,
			)
			currentNode := stopWatch()
			length := len(state)
			shape, shapeErr := parseOpenAICodexTicketShape(state)
			blocks := shape.Blocks

			// 1. 遇到 429 限流
			if status == http.StatusTooManyRequests {
				consecutiveFails++
				progressCallback(ManualHarvestProgress{
					Attempt:     attempt,
					MaxAttempts: req.MaxAttempts,
					Model:       model,
					Node:        currentNode,
					HTTPStatus:  status,
					Result:      "rate_limited",
					Level:       "WARN",
					Message:     fmt.Sprintf("上游限流了，进入冷静期 %d 秒后自动继续", req.RateLimitCooldownSeconds),
					Detail:      "HTTP 429 Too Many Requests",
				})
				recordCodexHarvestProbe(account, model, "rate_limited", currentNode, "429 Too Many Requests", status, length, blocks, expectedLength, expectedBlocks)
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(time.Duration(req.RateLimitCooldownSeconds) * time.Second):
				}
				continue
			}

			// 2. 命中降智标记 (312 / 356)
			if status == http.StatusOK && (blocks == 11 || blocks == 13 || length == 312 || length == 356) {
				consecutiveFails++
				progressCallback(ManualHarvestProgress{
					Attempt:     attempt,
					MaxAttempts: req.MaxAttempts,
					Model:       model,
					Node:        currentNode,
					HTTPStatus:  status,
					Length:      length,
					Blocks:      blocks,
					ExpectedLen: expectedLength,
					ExpectedBlk: expectedBlocks,
					Result:      "miss_degraded",
					Level:       "WARN",
					Message:     fmt.Sprintf("拿到的是降智票据（%d 字节 / %d 块），已拒收 → 换节点重试", length, blocks),
					Detail:      fmt.Sprintf("len=%d blk=%d · 合规应为 %d/%d", length, blocks, expectedLength, expectedBlocks),
				})
				recordCodexHarvestProbe(account, model, "probe_miss", currentNode, "degraded_state", status, length, blocks, expectedLength, expectedBlocks)

				if req.NodeSwitchRule == "312_only" || req.NodeSwitchRule == "312_or_2fail" {
					logger.L().Info("manual_harvest: node switch triggered by 312 degraded state")
					consecutiveFails = 0
				}

				if err := sleepWithContext(ctx, time.Duration(req.ProbeIntervalSeconds)*time.Second); err != nil {
					return err
				}
				continue
			}

			// 3. 网络或其它协议错误
			if perr != nil || status != http.StatusOK || shapeErr != nil {
				consecutiveFails++
				rawErr := "网络异常或未命中合规状态"
				if perr != nil {
					rawErr = perr.Error()
				} else if shapeErr != nil {
					rawErr = shapeErr.Error()
				}
				message, level, detail := describeCodexProbeFailure(rawErr, status, model, currentNode)
				progressCallback(ManualHarvestProgress{
					Attempt:     attempt,
					MaxAttempts: req.MaxAttempts,
					Model:       model,
					Node:        currentNode,
					HTTPStatus:  status,
					Length:      length,
					Result:      "error",
					Level:       level,
					Message:     message,
					Detail:      detail,
				})
				if err := sleepWithContext(ctx, time.Duration(req.ProbeIntervalSeconds)*time.Second); err != nil {
					return err
				}
				continue
			}

			// 4. 命中合规门票 (292 / 332)
			if blocks == expectedBlocks && length == expectedLength && strings.HasPrefix(state, openAICodexTicketStatePrefix) {
				consecutiveFails = 0
				now := time.Now()
				ticket := &openAICodexTicket{
					AccountID:  account.ID,
					Model:      model,
					State:      state,
					Length:     length,
					CapturedAt: now,
					ExpiresAt:  now.Add(time.Duration(cfg.TTLSeconds) * time.Second),
					Attempts:   attempt,
					Blocks:     shape.Blocks,
					IssuedAt:   shape.IssuedAt,
				}
				if expires := shape.IssuedAt.Add(time.Hour - 30*time.Second); expires.Before(ticket.ExpiresAt) {
					ticket.ExpiresAt = expires
				}

				s.storeOpenAICodexTicket(ctx, account, ticket)
				s.openaiCodexTicketProbeCooldown.Delete(openAICodexTicketKey(account.ID, model))
				recordCodexHarvestProbe(account, model, "success", currentNode, "", status, length, blocks, expectedLength, expectedBlocks)
				ticketsStored++

				progressCallback(ManualHarvestProgress{
					Attempt:       attempt,
					MaxAttempts:   req.MaxAttempts,
					Model:         model,
					Node:          currentNode,
					HTTPStatus:    status,
					Length:        length,
					Blocks:        blocks,
					ExpectedLen:   expectedLength,
					ExpectedBlk:   expectedBlocks,
					Result:        "hit",
					Level:         "OK",
					TicketsStored: ticketsStored,
					Message:       fmt.Sprintf("🎉 成功捕获合规门票（%d 字节 / %d 块），已持久化入库！", length, blocks),
					Detail:        fmt.Sprintf("len=%d blk=%d · node=%s", length, blocks, currentNode),
				})

				if req.StopOnSuccess {
					progressCallback(ManualHarvestProgress{
						Attempt:       attempt,
						MaxAttempts:   req.MaxAttempts,
						Result:        "hit",
						Level:         "OK",
						TicketsStored: ticketsStored,
						Done:          true,
						Message:       "达成【出票即停】条件，手动打票任务圆满结束。",
					})
					return nil
				}
			}

			if err := sleepWithContext(ctx, time.Duration(req.ProbeIntervalSeconds)*time.Second); err != nil {
				return err
			}
		}
	}

	progressCallback(ManualHarvestProgress{
		Attempt:       req.MaxAttempts,
		MaxAttempts:   req.MaxAttempts,
		Result:        "done",
		TicketsStored: ticketsStored,
		Done:          true,
		Level:         "WARN",
		Message:       fmt.Sprintf("已达到最大尝试次数（%d 次），打票任务结束。", req.MaxAttempts),
	})
	return nil
}

// describeCodexProbeFailure 把探针失败的内部错误翻译成用户看得懂的说明。
//
// 返回三段：通俗主文案（发生了什么 + 系统会怎么处理）、日志级别、原始技术细节。
// 技术细节不丢 —— 主文案看不懂时还能照着它排障。
func describeCodexProbeFailure(rawErr string, status int, model, node string) (message, level, detail string) {
	lower := strings.ToLower(rawErr)
	detailParts := make([]string, 0, 4)
	if status > 0 {
		detailParts = append(detailParts, fmt.Sprintf("HTTP %d", status))
	}
	if rawErr != "" {
		detailParts = append(detailParts, clipFlowText(rawErr, 220))
	}
	if model != "" {
		detailParts = append(detailParts, "model="+model)
	}
	if node != "" {
		detailParts = append(detailParts, "node="+node)
	}
	detail = strings.Join(detailParts, " · ")

	switch {
	case status == http.StatusUnauthorized:
		return "账号登录凭证已失效，需要重新登录该账号", "ERROR", detail
	case status == http.StatusForbidden:
		return "上游拒绝了本次请求（账号或模型被限制）→ 换节点重试", "WARN", detail
	case strings.Contains(lower, "invalid probe event"):
		return "节点返回的内容不是有效数据，可能被拦截了 → 换节点重试", "WARN", detail
	case strings.Contains(lower, "probe response failed"):
		return "上游明确返回失败（账号或模型被限制）→ 换节点重试", "WARN", detail
	case strings.Contains(lower, "invalid probe completion"):
		return "上游回复不完整就中断了 → 换节点重试", "WARN", detail
	case strings.Contains(lower, "invalid probe stream"):
		return "响应数据流损坏，读不下去 → 换节点重试", "WARN", detail
	case strings.Contains(lower, "unterminated probe event"):
		return "响应被中途截断（连接被掐断）→ 换节点重试", "WARN", detail
	case strings.Contains(lower, "probe did not complete successfully"):
		return "上游没给出完整结果就结束了 → 换节点重试", "WARN", detail
	case strings.Contains(lower, "deadline exceeded"), strings.Contains(lower, "client.timeout"),
		strings.Contains(lower, "timeout"), strings.Contains(lower, "timed out"):
		return "节点响应超时（超过设定时间没有回）→ 换节点重试", "WARN", detail
	case strings.Contains(lower, "no such host"), strings.Contains(lower, "connection refused"):
		return "出口节点连不上 → 换节点重试", "WARN", detail
	case strings.Contains(lower, "eof"), strings.Contains(lower, "connection reset"),
		strings.Contains(lower, "connection aborted"), strings.Contains(lower, "broken pipe"):
		return "节点连接被中断 → 换节点重试", "WARN", detail
	}
	return "本次探针没有拿到合规门票 → 换节点重试", "WARN", detail
}
