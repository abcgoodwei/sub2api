package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

type setCodexSkipHarvestRequest struct {
	SkipHarvest *bool `json:"skip_harvest" binding:"required"`
}

// GetCodexHarvestFlow returns the live harvest pipeline for the admin flow page.
// GET /api/v1/admin/accounts/codex-harvest-flow
func (h *AccountHandler) GetCodexHarvestFlow(c *gin.Context) {
	if h == nil {
		response.Error(c, http.StatusServiceUnavailable, "Account handler not available")
		return
	}
	ctx := c.Request.Context()
	var accounts []service.Account
	if h.adminService != nil {
		listed, err := h.adminService.ListAccountsForSchedulerScoreFilter(ctx, service.PlatformOpenAI, "", "", "", 0, "")
		if err != nil {
			response.ErrorFrom(c, err)
			return
		}
		accounts = listed
	}
	response.Success(c, service.BuildCodexHarvestFlow(ctx, h.cfg, h.codexTicketSettings, accounts))
}

// SetCodexSkipHarvest marks an account as harvest-excluded. Fail-closed gated
// routing then refuses leftover tickets on that account.
// PUT /api/v1/admin/accounts/:id/codex-skip-harvest
func (h *AccountHandler) SetCodexSkipHarvest(c *gin.Context) {
	if h == nil || h.adminService == nil {
		response.Error(c, http.StatusServiceUnavailable, "Account handler not available")
		return
	}
	accountID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || accountID <= 0 {
		response.BadRequest(c, "Invalid account ID")
		return
	}
	var req setCodexSkipHarvestRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.SkipHarvest == nil {
		response.BadRequest(c, "skip_harvest is required")
		return
	}
	if err := h.adminService.UpdateAccountExtra(c.Request.Context(), accountID, map[string]any{
		service.OpenAICodexSkipHarvestExtraKey: *req.SkipHarvest,
	}); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"account_id": accountID, "skip_harvest": *req.SkipHarvest})
}

type updateCodexHarvestConfigRequest struct {
	ProbeIntervalSeconds  *int `json:"probe_interval_seconds"`
	MaxProbesPerRound     *int `json:"max_probes_per_round"`
	CooldownSeconds       *int `json:"cooldown_seconds"`
	AttemptTimeoutSeconds *int `json:"attempt_timeout_seconds"`
	RefreshBeforeSeconds  *int `json:"refresh_before_seconds"`
}

// UpdateCodexHarvestConfig 保存管理员在打票流程页配置的自动打票参数
// PUT /api/v1/admin/accounts/codex-harvest-flow/config
func (h *AccountHandler) UpdateCodexHarvestConfig(c *gin.Context) {
	if h == nil || h.codexTicketSettings == nil {
		response.Error(c, http.StatusServiceUnavailable, "Setting service not available")
		return
	}
	var req updateCodexHarvestConfigRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid parameters")
		return
	}

	ctx := c.Request.Context()

	// 一次性收集本次要改的键，最后用 SetMultiple 批量落库：原先 5 次串行 Set
	// 要 5 次 DB 往返，是保存接口慢的主因之一。
	updates := make(map[string]string, 5)
	if req.ProbeIntervalSeconds != nil && *req.ProbeIntervalSeconds >= 10 && *req.ProbeIntervalSeconds <= 1800 {
		updates[service.SettingKeyOpenAICodexTicketProbeIntervalSeconds] = strconv.Itoa(*req.ProbeIntervalSeconds)
	}
	if req.MaxProbesPerRound != nil && *req.MaxProbesPerRound >= 1 && *req.MaxProbesPerRound <= 50 {
		updates[service.SettingKeyOpenAICodexTicketMaxProbesPerRound] = strconv.Itoa(*req.MaxProbesPerRound)
	}
	if req.CooldownSeconds != nil && *req.CooldownSeconds >= 5 && *req.CooldownSeconds <= 600 {
		updates[service.SettingKeyOpenAICodexTicketCooldownSeconds] = strconv.Itoa(*req.CooldownSeconds)
	}
	if req.AttemptTimeoutSeconds != nil && *req.AttemptTimeoutSeconds >= 5 && *req.AttemptTimeoutSeconds <= 60 {
		updates[service.SettingKeyOpenAICodexTicketAttemptTimeoutSeconds] = strconv.Itoa(*req.AttemptTimeoutSeconds)
	}
	if req.RefreshBeforeSeconds != nil && *req.RefreshBeforeSeconds >= 60 && *req.RefreshBeforeSeconds <= 1800 {
		updates[service.SettingKeyOpenAICodexTicketRefreshBeforeSeconds] = strconv.Itoa(*req.RefreshBeforeSeconds)
	}

	// 写失败必须如实报错，不能再像以前那样静默 _ = 忽略，否则用户看到"成功"其实没落库。
	if err := h.codexTicketSettings.SetRawKeys(ctx, updates); err != nil {
		response.Error(c, http.StatusInternalServerError, "保存失败: "+err.Error())
		return
	}

	// 先失效缓存再唤醒：后台采票循环用同一个 SettingService，改完参数后应当立即
	// 按新节拍重排定时器，而不是等原来的（可能长达 1800 秒的）周期走完。
	h.codexTicketSettings.InvalidateCodexHarvestCaches()
	h.codexTicketSettings.NotifyCodexHarvest()
	response.Success(c, gin.H{"message": "自动打票参数已成功保存并生效"})
}

// ManualCodexHarvest 接收前端参数，以 SSE 流式实时回传定向单号打票过程
// POST /api/v1/admin/accounts/:id/manual-harvest
func (h *AccountHandler) ManualCodexHarvest(c *gin.Context) {
	if h == nil || h.openAIGatewayService == nil {
		response.Error(c, http.StatusServiceUnavailable, "Gateway service not available")
		return
	}
	accountID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || accountID <= 0 {
		response.BadRequest(c, "Invalid account ID")
		return
	}

	var req service.ManualHarvestRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid parameters")
		return
	}
	req.AccountID = accountID

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")

	ctx := c.Request.Context()
	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		response.Error(c, http.StatusInternalServerError, "Streaming unsupported")
		return
	}

	writeProgress := func(p service.ManualHarvestProgress) {
		data, err := json.Marshal(p)
		if err != nil {
			return
		}
		_, _ = fmt.Fprintf(c.Writer, "data: %s\n\n", data)
		flusher.Flush()
	}
	// The SSE headers are already committed, so a rejected request is reported as
	// a final error event instead of an HTTP status. Client-side cancellation is
	// expected (the stop button) and must not be surfaced as a failure.
	if err := h.openAIGatewayService.ExecuteManualHarvest(ctx, req, writeProgress); err != nil && !errors.Is(err, context.Canceled) {
		writeProgress(service.ManualHarvestProgress{
			Result:  "error",
			Level:   "ERROR",
			Message: err.Error(),
			Done:    true,
		})
	}
}
