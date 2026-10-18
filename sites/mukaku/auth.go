package mukaku

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"web2rss/shared"
)

// tokenSuspendWindow 令牌被服务端拒绝后的静默时长：期间直接按游客请求，
// 到点自动重试令牌（站点临时异常恢复后可自动回到会员通道）
const tokenSuspendWindow = time.Hour

// tokenSuspendedUntil 令牌静默期截止时间（Unix 秒，内存态）
var tokenSuspendedUntil atomic.Int64

// tokenSuspended 判断令牌是否处于静默期
func tokenSuspended() bool {
	return time.Now().Unix() < tokenSuspendedUntil.Load()
}

// suspendToken 进入令牌静默期，并返回静默时长以便日志说明
func suspendToken() time.Duration {
	tokenSuspendedUntil.Store(time.Now().Add(tokenSuspendWindow).Unix())
	return tokenSuspendWindow
}

// jwtExpiry 本地解析 JWT 的 exp 声明（仅解码载荷，不校验签名）。
// 返回 ok=false 表示不是标准 JWT 或不含 exp
func jwtExpiry(token string) (time.Time, bool) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return time.Time{}, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return time.Time{}, false
	}
	var claims struct {
		Exp float64 `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil || claims.Exp <= 0 {
		return time.Time{}, false
	}
	return time.Unix(int64(claims.Exp), 0), true
}

// authHeaders 构造携带 VIP 令牌的请求头；未配置令牌时返回 nil
func authHeaders(ctx *shared.SiteContext) map[string]string {
	if ctx.AccessToken == "" {
		return nil
	}
	return map[string]string{"Authorization": "Bearer " + ctx.AccessToken}
}

// tokenParam 返回附加在 URL 上的 access_token 查询参数；未配置令牌时为空串
func tokenParam(ctx *shared.SiteContext) string {
	if ctx.AccessToken == "" {
		return ""
	}
	return "&access_token=" + url.QueryEscape(ctx.AccessToken)
}

// InitInfo getInfo 接口返回的账号信息
type InitInfo struct {
	User *UserInfo `json:"user"`
}

// UserInfo 账号信息（字段与站点前端存储一致）
type UserInfo struct {
	Username       string      `json:"username"`
	Nickname       string      `json:"nickname"`
	IsVip          int         `json:"is_vip"`           // 2 表示 VIP
	IsPermanentVip interface{} `json:"is_permanent_vip"` // 布尔或数字
	VipType        int         `json:"vip_type"`         // 4 表示永久
	VipExpireAt    string      `json:"vip_expire_at"`
}

// StartupCheck 启动时的令牌检查：未配置令牌时提示游客模式；
// 配置了则异步校验有效性并输出账号与 VIP 状态，便于确认会员是否生效
func StartupCheck(ctx *shared.SiteContext) {
	if ctx.AccessToken == "" {
		log.Printf("[mukaku] 未配置 MUKAKU_ACCESS_TOKEN，按游客模式抓取公开种子流")
		return
	}

	reqCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// 令牌为 JWT（三段、含两个点），格式不符多为复制不完整，先行提示
	if strings.Count(ctx.AccessToken, ".") != 2 {
		log.Printf("[mukaku] 注意：access_token 格式更像不完整的值（正常为三段式 JWT），请确认复制完整")
	}

	// 本地解析有效期，便于提前掌握续期时间
	if exp, ok := jwtExpiry(ctx.AccessToken); ok {
		if remain := time.Until(exp); remain > 0 {
			log.Printf("[mukaku] 令牌有效期至 %s（剩余 %d 天）",
				exp.In(ctx.CSTZone).Format("2006-01-02 15:04"), remainDays(remain))
		} else {
			log.Printf("[mukaku] 令牌已于 %s 过期，请重新登录获取新令牌",
				exp.In(ctx.CSTZone).Format("2006-01-02 15:04"))
		}
	}

	apiURL := fmt.Sprintf("%s/getInfo?app_id=%s&identity=%s%s", baseURL+"/prod/api/v1", appID, identity, tokenParam(ctx))
	resp, err := shared.HTTPGetWithHeaders(reqCtx, ctx.Client, apiURL, ctx.UserAgents, 0, ctx.RetryInterval, authHeaders(ctx))
	if err != nil {
		log.Printf("[mukaku] access_token 校验请求异常：%v（继续按游客模式抓取）", err)
		return
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("[mukaku] access_token 校验响应读取失败：%v（继续按游客模式抓取）", err)
		return
	}

	var info struct {
		ApiResponse
		Data *InitInfo `json:"data"`
	}
	if err := json.Unmarshal(body, &info); err != nil || info.Data == nil || info.Data.User == nil {
		msg := info.Message
		if msg == "" {
			msg = "响应格式不符"
		}
		log.Printf("[mukaku] access_token 无效或已过期：%s（继续按游客模式抓取，请重新登录更新令牌）", msg)
		return
	}

	user := info.Data.User
	name := user.Nickname
	if name == "" {
		name = user.Username
	}
	if name == "" {
		name = "未知账号"
	}

	vip := "否"
	switch {
	case user.VipType == 4 || truthy(user.IsPermanentVip):
		vip = "是（永久）"
	case user.IsVip == 2 && user.VipExpireAt != "":
		vip = "是（到期 " + user.VipExpireAt + "）"
	case user.IsVip == 2:
		vip = "是"
	}

	log.Printf("[mukaku] access_token 校验成功：账号 %s，VIP：%s", name, vip)
}

// truthy 判断接口返回的布尔字段（可能是 true / 1 / "1"）
func truthy(v interface{}) bool {
	switch val := v.(type) {
	case bool:
		return val
	case float64:
		return val != 0
	case string:
		return val != "" && val != "0" && val != "false"
	default:
		return false
	}
}

// remainDays 将剩余时长折算为天数（四舍五入，不足一天按 1 天计）
func remainDays(d time.Duration) int {
	days := int(d.Hours()/24 + 0.5)
	if days < 1 {
		days = 1
	}
	return days
}

// expiryWarnWindow 临近到期开始提醒的窗口
const expiryWarnWindow = 7 * 24 * time.Hour

// WatchToken 后台例行检查令牌有效期：临近过期时提醒更换，避免会员通道静默掉线
func WatchToken(ctx *shared.SiteContext) {
	if ctx.AccessToken == "" {
		return
	}
	exp, ok := jwtExpiry(ctx.AccessToken)
	if !ok {
		return
	}

	ticker := time.NewTicker(6 * time.Hour)
	defer ticker.Stop()
	for range ticker.C {
		remain := time.Until(exp)
		if remain > 0 && remain < expiryWarnWindow {
			log.Printf("[mukaku] 令牌将于 %s 过期（剩余 %d 天），请提前重新登录更换",
				exp.In(ctx.CSTZone).Format("2006-01-02 15:04"), remainDays(remain))
		}
	}
}
