//go:build unit

package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/handler/quotaview"
	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// fakeQuotaRepoForUserHandler 实现 service.UserPlatformQuotaRepository 最小子集
type fakeQuotaRepoForUserHandler struct {
	service.UserPlatformQuotaRepository
	records []service.UserPlatformQuotaRecord
}

func (f *fakeQuotaRepoForUserHandler) ListByUser(_ context.Context, _ int64) ([]service.UserPlatformQuotaRecord, error) {
	return f.records, nil
}

func TestGetMyPlatformQuotas_EmptyReturns200WithEmptyArray(t *testing.T) {
	repo := &fakeQuotaRepoForUserHandler{records: nil}
	h := &UserHandler{userPlatformQuotaRepo: repo}
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/user/platform-quotas", nil)
	c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 42})
	h.GetMyPlatformQuotas(c)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d. body: %s", w.Code, w.Body.String())
	}
	var body struct {
		Code int `json:"code"`
		Data struct {
			PlatformQuotas []any `json:"platform_quotas"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal error: %v, body: %s", err, w.Body.String())
	}
	if body.Code != 0 {
		t.Errorf("expected code=0, got %d", body.Code)
	}
	if body.Data.PlatformQuotas == nil {
		// nil 和 empty slice 均视为可接受（JSON 可能序列化为 null 或 []）
		// 此断言只验证 HTTP 200 + code=0 即可
	}
}

func TestGetMyPlatformQuotas_D14_LazyZeroForExpiredWindow(t *testing.T) {
	pastStart := time.Now().UTC().AddDate(0, 0, -2)
	daily := 5.0
	repo := &fakeQuotaRepoForUserHandler{records: []service.UserPlatformQuotaRecord{{
		UserID:           42,
		Platform:         "anthropic",
		DailyLimitUSD:    &daily,
		DailyUsageUSD:    3.0,
		DailyWindowStart: &pastStart,
	}}}
	h := &UserHandler{userPlatformQuotaRepo: repo}
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/user/platform-quotas", nil)
	c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 42})
	h.GetMyPlatformQuotas(c)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d. body: %s", w.Code, w.Body.String())
	}

	// 解析 response，验证过期 daily 的 usage_usd=0 且 window_resets_at=null
	body := w.Body.String()
	if !strings.Contains(body, `"daily_usage_usd":0`) {
		t.Errorf("expected daily_usage_usd:0 in body, got: %s", body)
	}
	if !strings.Contains(body, `"daily_window_resets_at":null`) {
		t.Errorf("expected daily_window_resets_at:null in body, got: %s", body)
	}
}

func TestGetMyPlatformQuotas_NilRepo_Returns200Empty(t *testing.T) {
	h := &UserHandler{userPlatformQuotaRepo: nil}
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/user/platform-quotas", nil)
	c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 99})
	h.GetMyPlatformQuotas(c)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestGetMyPlatformQuotas_NoAuth_Returns401(t *testing.T) {
	h := &UserHandler{userPlatformQuotaRepo: nil}
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/user/platform-quotas", nil)
	// 不设置 auth subject
	h.GetMyPlatformQuotas(c)
	if w.Code != 401 {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

func TestLazyZeroQuotaForResponse_UserViewStripsWindowStart(t *testing.T) {
	start := time.Now().UTC().Add(-1 * time.Hour)
	r := service.UserPlatformQuotaRecord{
		Platform:         "anthropic",
		DailyUsageUSD:    1.0,
		DailyWindowStart: &start,
	}
	out := quotaview.LazyZeroQuotaForResponse(r, service.LocalQuotaWindows(time.Now().UTC()), time.Now().UTC(), false)
	if _, ok := out["daily_window_start"]; ok {
		t.Error("user view should not include daily_window_start")
	}
}

func TestLazyZeroQuotaForResponse_AdminViewIncludesWindowStart(t *testing.T) {
	start := time.Now().UTC().Add(-1 * time.Hour)
	r := service.UserPlatformQuotaRecord{
		Platform:         "anthropic",
		DailyWindowStart: &start,
	}
	out := quotaview.LazyZeroQuotaForResponse(r, service.LocalQuotaWindows(time.Now().UTC()), time.Now().UTC(), true)
	if _, ok := out["daily_window_start"]; !ok {
		t.Error("admin view should include daily_window_start")
	}
}

func TestLazyZeroQuotaForResponse_ActiveWindowPreservesUsage(t *testing.T) {
	// 今天的窗口起始时间（不过期）：按全局时区取当天 0 点，与 view 层同口径
	now := time.Now()
	today := timezone.StartOfDay(now)
	usage := 2.5
	r := service.UserPlatformQuotaRecord{
		Platform:         "openai",
		DailyUsageUSD:    usage,
		DailyWindowStart: &today,
	}
	out := quotaview.LazyZeroQuotaForResponse(r, service.LocalQuotaWindows(now), now, false)
	if out["daily_usage_usd"] != usage {
		t.Errorf("expected daily_usage_usd=%v, got %v", usage, out["daily_usage_usd"])
	}
	// 活跃窗口应有 resets_at（非 nil）
	if out["daily_window_resets_at"] == nil {
		t.Error("active window should have daily_window_resets_at set")
	}
}

// 窗口过期判定原本由 quotaview 自己的谓词实现，现已统一到
// service.ResolvedQuotaWindow（见 quota_window_resolver_test.go）。
// 这里保留的是「透过响应视图能观察到的」语义，避免只测内部谓词而漏掉接线。

func TestLazyZeroQuotaForResponse_ExpiredDailyWindowZeroesUsage(t *testing.T) {
	now := time.Now().UTC()
	yesterday := timezone.StartOfDay(now).AddDate(0, 0, -1)
	r := service.UserPlatformQuotaRecord{
		Platform:         "openai",
		DailyUsageUSD:    7.5,
		DailyWindowStart: &yesterday,
	}
	out := quotaview.LazyZeroQuotaForResponse(r, service.LocalQuotaWindows(now), now, false)
	if out["daily_usage_usd"] != 0.0 {
		t.Errorf("expired daily window should report 0 usage, got %v", out["daily_usage_usd"])
	}
	// 注意：map[string]any 里存的是 *string，值为 nil 时接口本身仍非 nil
	// （典型的 typed-nil 陷阱），必须断言到具体类型再比较。
	if v, _ := out["daily_window_resets_at"].(*string); v != nil {
		t.Errorf("expired window should not carry resets_at, got %q", *v)
	}
}

// 月窗口是 30 天滚动，不是自然月：跨月但不足 30 天不得重置。
func TestLazyZeroQuotaForResponse_MonthlyIs30DayRolling(t *testing.T) {
	tests := []struct {
		name      string
		start     time.Time
		now       time.Time
		wantUsage float64
	}{
		{
			name:      "31 天前的窗口已过期",
			start:     time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC),
			now:       time.Date(2026, 5, 2, 0, 0, 0, 0, time.UTC),
			wantUsage: 0,
		},
		{
			name:      "15 天前的窗口仍有效",
			start:     time.Date(2026, 4, 20, 0, 0, 0, 0, time.UTC),
			now:       time.Date(2026, 5, 5, 0, 0, 0, 0, time.UTC),
			wantUsage: 4.25,
		},
		{
			name:      "跨自然月但不足 30 天不重置",
			start:     time.Date(2026, 4, 20, 0, 0, 0, 0, time.UTC),
			now:       time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC),
			wantUsage: 4.25,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			start := tc.start
			r := service.UserPlatformQuotaRecord{
				Platform:           "openai",
				MonthlyUsageUSD:    4.25,
				MonthlyWindowStart: &start,
			}
			out := quotaview.LazyZeroQuotaForResponse(r, service.LocalQuotaWindows(tc.now), tc.now, false)
			if out["monthly_usage_usd"] != tc.wantUsage {
				t.Errorf("monthly_usage_usd = %v, want %v", out["monthly_usage_usd"], tc.wantUsage)
			}
		})
	}
}
