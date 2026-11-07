package cpa

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/WenPaul/cpa-plugin-step2api/internal/pluginapi"
	"github.com/WenPaul/cpa-plugin-step2api/internal/stepstudio"
)

func (p *Plugin) handleQuotaIdentifier() (any, error) {
	return rpcIdentifierResponse{
		Identifier:          ProviderID,
		Identifiers:         []string{"step2api", "stepstudio", "stepfun"},
		CredentialProviders: []string{"step2api", "stepstudio", "stepfun"},
	}, nil
}

func (p *Plugin) handleQuotaDescribe() (any, error) {
	return pluginapi.QuotaDescribeResponse{
		SupportedProviders: []string{"step2api", "stepstudio", "stepfun"},
		DisplayName:        "StepFun AI Studio Quota",
		SupportsReset:      false,
	}, nil
}

func (p *Plugin) handleQuotaFetch(raw []byte) (any, error) {
	var req pluginapi.QuotaFetchRequest
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &req)
	}

	token := p.extractToken(req.StorageJSON, req.Metadata)
	webID := ""
	if req.Metadata != nil {
		if w, ok := req.Metadata["web_id"].(string); ok {
			webID = strings.TrimSpace(w)
		}
	}

	oauthDir := resolveOAuthDir()
	accountFile := ""
	if req.AuthID != "" {
		target := filepath.Join(oauthDir, "step2api-"+req.AuthID+".json")
		if _, err := os.Stat(target); err == nil {
			accountFile = target
		}
	}
	if accountFile == "" {
		if files, err := filepath.Glob(filepath.Join(oauthDir, "step2api-*.json")); err == nil && len(files) > 0 {
			accountFile = files[len(files)-1]
		}
	}

	if accountFile != "" {
		if data, err := os.ReadFile(accountFile); err == nil {
			var payload AuthFilePayload
			if err := json.Unmarshal(data, &payload); err == nil {
				if token == "" && payload.OasisToken != "" {
					token = payload.OasisToken
				}
				if webID == "" && payload.WebID != "" {
					webID = payload.WebID
				}
			}
		}
	}

	if token == "" {
		return pluginapi.QuotaFetchResponse{}, fmt.Errorf("no Oasis-Token found for credential %s", req.AuthID)
	}

	if strings.HasPrefix(token, "sample_") || strings.HasPrefix(token, "test_") {
		return p.buildFallbackQuota(token), nil
	}

	// 1. Refresh token if expired or near expiration
	access, refresh, _ := stepstudio.SplitToken(token)
	if webID == "" {
		webID = stepstudio.ExtractDeviceID(refresh)
		if webID == "" {
			webID = stepstudio.ExtractDeviceID(access)
		}
	}

	// 2. Query upstream APIs with bounded timeout
	fetchCtx, cancel := context.WithTimeout(p.ctx, 8*time.Second)
	defer cancel()

	if stepstudio.NeedsRefresh(token, 180*time.Second) {
		newTok, errRef := stepstudio.RefreshToken(fetchCtx, nil, p.config.BaseURL, token, webID)
		if errRef == nil && newTok != "" {
			token = newTok
			p.saveTokenToDisk(accountFile, token, webID)
		}
	}

	client := stepstudio.NewClient(p.config.BaseURL, token, webID, nil)

	rateLimit, errRate := client.QueryRateLimit(fetchCtx)
	if errRate != nil {
		if newTok, errRef := stepstudio.RefreshToken(fetchCtx, nil, p.config.BaseURL, token, webID); errRef == nil && newTok != "" {
			token = newTok
			client.SetToken(newTok)
			p.saveTokenToDisk(accountFile, token, webID)
			rateLimit, errRate = client.QueryRateLimit(fetchCtx)
		}
	}

	planStatus, _ := client.GetStepPlanStatus(fetchCtx)
	platformPlan, _ := client.QueryStepPlanRateLimit(fetchCtx)
	accountBalance, _ := client.QueryAccountBalance(fetchCtx)

	// If rateLimit successfully fetched:
	if errRate == nil && rateLimit != nil {
		hasPlan := planStatus != nil && planStatus.Subscription != nil && planStatus.Subscription.Name != ""
		planName := "免费版"
		tierName := "免费用户"
		if hasPlan {
			planName = planStatus.Subscription.Name
			tierName = planName + " (Token Plan)"
		}

		resp := pluginapi.QuotaFetchResponse{
			Subscription: &pluginapi.QuotaSubscription{
				Plan:     planName,
				TierName: tierName,
				TierID:   planName,
			},
		}

		// --- Group 1: 开放平台 (API / Step Plan) ---
		var platformBuckets []pluginapi.QuotaBucket
		if platformPlan != nil && platformPlan.PlanCreditRateLimit != nil {
			pRate := platformPlan.PlanCreditRateLimit
			remFraction := pRate.SubscriptionCreditLeftRate

			nextResetStr := ""
			if pRate.SubscriptionCreditResetTime != "" && pRate.SubscriptionCreditResetTime != "0" {
				if sec, err := strconv.ParseInt(pRate.SubscriptionCreditResetTime, 10, 64); err == nil && sec > 0 {
					nextResetStr = time.Unix(sec, 0).Format(time.RFC3339)
				}
			}

			desc := "月度额度"
			if len(pRate.CreditBuckets) > 0 {
				b := pRate.CreditBuckets[0]
				tot, _ := strconv.ParseFloat(b.CreditTotal, 64)
				res, _ := strconv.ParseFloat(b.CreditResidual, 64)
				expStr := ""
				if b.ExpireAt != "" && b.ExpireAt != "0" {
					if sec, err := strconv.ParseInt(b.ExpireAt, 10, 64); err == nil && sec > 0 {
						expStr = " · 周期至 " + time.Unix(sec, 0).Format("2006-01-02")
					}
				}
				desc = fmt.Sprintf("剩余 %.2f 亿 / %.2f 亿 Credits%s", res/1e8, tot/1e8, expStr)
			}

			platformBuckets = append(platformBuckets, pluginapi.QuotaBucket{
				Window:            "Plus 套餐月池",
				RemainingFraction: remFraction,
				ResetTime:         nextResetStr,
				Description:       desc,
			})
		}

		if len(platformBuckets) > 0 {
			resp.Groups = append(resp.Groups, pluginapi.QuotaGroup{
				DisplayName: "开放平台 (API / Step Plan)",
				Buckets:     platformBuckets,
			})
		}

		// --- Group 2: AI Studio (网页会话) ---
		var studioBuckets []pluginapi.QuotaBucket
		now := time.Now()
		nextMonth := time.Date(now.Year(), now.Month()+1, 1, 0, 0, 0, 0, now.Location())

		studioBuckets = append(studioBuckets, pluginapi.QuotaBucket{
			Window:            "Studio 免费额度",
			RemainingFraction: rateLimit.FreeRate,
			ResetTime:         nextMonth.Format(time.RFC3339),
			Description:       "每月 1 日刷新 · 会话优先消耗",
		})

		studioPlanResetStr := ""
		if rateLimit.CreditRateLimit != nil && rateLimit.CreditRateLimit.SubscriptionCreditResetTime != "" && rateLimit.CreditRateLimit.SubscriptionCreditResetTime != "0" {
			if sec, err := strconv.ParseInt(rateLimit.CreditRateLimit.SubscriptionCreditResetTime, 10, 64); err == nil && sec > 0 {
				studioPlanResetStr = time.Unix(sec, 0).Format(time.RFC3339)
			}
		}
		if hasPlan {
			studioBuckets = append(studioBuckets, pluginapi.QuotaBucket{
				Window:            "Studio Plus 额度",
				RemainingFraction: 1.0,
				ResetTime:         studioPlanResetStr,
				Description:       "Plus 待启用 · 免费额度用完后自动启用",
			})
		}

		resp.Groups = append(resp.Groups, pluginapi.QuotaGroup{
			DisplayName: "AI Studio (网页会话)",
			Buckets:     studioBuckets,
		})

		// --- Summary 指标与资产 ---
		if accountBalance != nil {
			if bVal, err := strconv.ParseFloat(accountBalance.Balance, 64); err == nil {
				resp.Summary = append(resp.Summary, pluginapi.QuotaMetric{
					Key:      "platform_balance",
					Label:    "开放平台现金余额",
					Value:    bVal / 100.0,
					Format:   "currency",
					Currency: "CNY",
				})
			}
			if cVal, err := strconv.ParseFloat(accountBalance.CostTotal, 64); err == nil {
				resp.Summary = append(resp.Summary, pluginapi.QuotaMetric{
					Key:      "cost_total",
					Label:    "累计消费",
					Value:    cVal / 100.0,
					Format:   "currency",
					Currency: "CNY",
				})
			}
		}

		freeUsedPct := math.Max(0, math.Min(100, (1.0-rateLimit.FreeRate)*100))
		resp.Summary = append(resp.Summary, pluginapi.QuotaMetric{
			Key:    "free_used",
			Label:  "Studio 免费已用",
			Value:  math.Round(freeUsedPct*100) / 100,
			Unit:   "%",
			Format: "number",
		})

		if planStatus != nil && planStatus.Subscription != nil && planStatus.Subscription.ExpiredAt != "" {
			if sec, err := strconv.ParseInt(planStatus.Subscription.ExpiredAt, 10, 64); err == nil && sec > 0 {
				expStr := time.Unix(sec, 0).Format("2006-01-02")
				resp.Summary = append(resp.Summary, pluginapi.QuotaMetric{
					Key:    "plan_expiry",
					Label:  "Plus 有效期至 " + expStr,
					Value:  100,
					Unit:   "%",
					Format: "number",
				})
			}
		}

		return resp, nil
	}

	return p.buildFallbackQuota(token), nil
}

func (p *Plugin) buildFallbackQuota(token string) pluginapi.QuotaFetchResponse {
	access, refresh, _ := stepstudio.SplitToken(token)
	accessExp := stepstudio.GetTokenExpiration(access)
	refreshExp := stepstudio.GetTokenExpiration(refresh)
	remFrac := 1.0
	if !accessExp.IsZero() && time.Now().After(accessExp) {
		remFrac = 0.0
	}
	resetTimeStr := ""
	if !refreshExp.IsZero() {
		resetTimeStr = refreshExp.Format(time.RFC3339)
	}

	return pluginapi.QuotaFetchResponse{
		Subscription: &pluginapi.QuotaSubscription{
			Plan:     "StepFun AI Studio",
			TierName: "网页端会话凭据",
		},
		Groups: []pluginapi.QuotaGroup{
			{
				DisplayName: "StepFun 会话额度",
				Buckets: []pluginapi.QuotaBucket{
					{
						Window:            "会话有效期",
						RemainingFraction: remFrac,
						ResetTime:         resetTimeStr,
						Description:       "自动续期保障中",
					},
				},
			},
		},
		Summary: []pluginapi.QuotaMetric{
			{
				Key:    "status",
				Label:  "凭据状态",
				Value:  1,
				Unit:   "正常",
				Format: "number",
			},
		},
	}
}

func (p *Plugin) saveTokenToDisk(fpath, token, webID string) {
	if fpath == "" || token == "" {
		return
	}
	data, err := os.ReadFile(fpath)
	if err != nil {
		return
	}
	var payload AuthFilePayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return
	}
	payload.OasisToken = token
	if webID != "" {
		payload.WebID = webID
	}
	payload.UpdatedAt = time.Now().Format(time.RFC3339)
	if updated, errM := json.MarshalIndent(payload, "", "  "); errM == nil {
		_ = os.WriteFile(fpath, updated, 0644)
	}
}
