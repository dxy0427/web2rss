package mukaku

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"web2rss/shared"
)

// tokenFile 续期令牌的持久化位置；容器挂载 /data 卷后重建也能保留
const tokenFile = "/data/mukaku_token.txt"

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

// resumeToken 解除令牌静默期（续期成功后立即恢复会员通道）
func resumeToken() {
	tokenSuspendedUntil.Store(0)
}

var (
	tokenMu      sync.RWMutex
	runtimeToken string // 运行期令牌（页面续期后更新），为空则回退环境变量
)

// curToken 返回当前生效令牌：优先运行期令牌，其次环境变量
func curToken(ctx *shared.SiteContext) string {
	tokenMu.RLock()
	t := runtimeToken
	tokenMu.RUnlock()
	if t != "" {
		return t
	}
	return ctx.AccessToken
}

// adoptToken 采用新令牌：更新运行期值、解除静默期并尽力持久化
func adoptToken(t string) {
	tokenMu.Lock()
	runtimeToken = t
	tokenMu.Unlock()
	resumeToken()
	persistToken(t)
}

// persistToken 写入令牌文件；失败只提示，不影响本次运行
func persistToken(t string) {
	if err := os.MkdirAll(filepath.Dir(tokenFile), 0o755); err != nil {
		log.Printf("[mukaku] 令牌持久化失败（%v），续期结果重启后失效", err)
		return
	}
	if err := os.WriteFile(tokenFile, []byte(t), 0o600); err != nil {
		log.Printf("[mukaku] 令牌持久化失败（%v），续期结果重启后失效", err)
	}
}

// InitToken 启动时载入持久化令牌；与环境变量令牌同时存在时取有效期更晚者
func InitToken(ctx *shared.SiteContext) {
	data, err := os.ReadFile(tokenFile)
	if err != nil {
		return
	}
	fileToken := strings.TrimSpace(string(data))
	if fileToken == "" {
		return
	}

	if ctx.AccessToken == "" {
		adoptToken(fileToken)
		log.Printf("[mukaku] 已载入持久化令牌")
		return
	}

	fileExp, fileOK := jwtExpiry(fileToken)
	envExp, envOK := jwtExpiry(ctx.AccessToken)
	if fileOK && (!envOK || fileExp.After(envExp)) {
		adoptToken(fileToken)
		log.Printf("[mukaku] 持久化令牌比环境变量令牌更新，已采用持久化令牌")
	}
}

// authHeaders 构造携带 VIP 令牌的请求头；无令牌时返回 nil
func authHeaders(ctx *shared.SiteContext) map[string]string {
	t := curToken(ctx)
	if t == "" {
		return nil
	}
	return map[string]string{"Authorization": "Bearer " + t}
}

// tokenParam 返回附加在 URL 上的 access_token 查询参数；无令牌时为空串
func tokenParam(ctx *shared.SiteContext) string {
	t := curToken(ctx)
	if t == "" {
		return ""
	}
	return "&access_token=" + url.QueryEscape(t)
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

// fetchUserInfo 拉取账号信息，同时用于校验令牌有效性
func fetchUserInfo(ctx *shared.SiteContext) (*UserInfo, error) {
	reqCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	apiURL := fmt.Sprintf("%s/getInfo?app_id=%s&identity=%s%s", baseURL+"/prod/api/v1", appID, identity, tokenParam(ctx))
	resp, err := shared.HTTPGetWithHeaders(reqCtx, ctx.Client, apiURL, ctx.UserAgents, 0, ctx.RetryInterval, authHeaders(ctx))
	if err != nil {
		return nil, fmt.Errorf("请求失败: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("读取响应失败: %w", err)
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
		return nil, fmt.Errorf("%s", msg)
	}
	return info.Data.User, nil
}

// displayName 账号展示名（昵称优先）
func displayName(u *UserInfo) string {
	if u.Nickname != "" {
		return u.Nickname
	}
	if u.Username != "" {
		return u.Username
	}
	return "未知账号"
}

// vipDesc 账号 VIP 状态描述
func vipDesc(u *UserInfo) string {
	switch {
	case u.VipType == 4 || truthy(u.IsPermanentVip):
		return "是（永久）"
	case u.IsVip == 2 && u.VipExpireAt != "":
		return "是（到期 " + u.VipExpireAt + "）"
	case u.IsVip == 2:
		return "是"
	default:
		return "否"
	}
}

// fetchCaptcha 获取图形验证码，返回图片（dataURL）与校验 key
func fetchCaptcha(ctx *shared.SiteContext) (string, string, error) {
	reqCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	apiURL := fmt.Sprintf("%s/getCaptcha?app_id=%s&identity=%s", baseURL+"/prod/api/v1", appID, identity)
	// retryMax=2：验证码接口偶发抖动时自动重试，避免用户看到“获取失败”
	resp, err := shared.HTTPGetWithHeaders(reqCtx, ctx.Client, apiURL, ctx.UserAgents, 2, ctx.RetryInterval,
		map[string]string{"Accept-Language": "zh_CN"})
	if err != nil {
		return "", "", fmt.Errorf("请求失败: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", "", fmt.Errorf("读取响应失败: %w", err)
	}

	var r struct {
		ApiResponse
		Data struct {
			Img string `json:"img"`
			Key string `json:"key"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &r); err != nil || r.Data.Img == "" || r.Data.Key == "" {
		return "", "", fmt.Errorf("验证码响应异常")
	}
	return r.Data.Img, r.Data.Key, nil
}

// loginWithCaptcha 使用配置的账号密码与人工输入的验证码登录，返回新令牌
func loginWithCaptcha(ctx *shared.SiteContext, code, key string) (string, error) {
	if ctx.Username == "" || ctx.Password == "" {
		return "", fmt.Errorf("未配置账号密码")
	}

	reqCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	payload, err := json.Marshal(map[string]string{
		"username": ctx.Username,
		"password": ctx.Password,
		"code":     code,
		"key":      key,
	})
	if err != nil {
		return "", fmt.Errorf("构造登录请求失败")
	}

	apiURL := fmt.Sprintf("%s/login?app_id=%s&identity=%s", baseURL+"/prod/api/v1", appID, identity)
	resp, err := shared.HTTPPostWithHeaders(reqCtx, ctx.Client, apiURL, "application/json;charset=UTF-8", payload,
		ctx.UserAgents, 0, ctx.RetryInterval, map[string]string{"Accept-Language": "zh_CN"})
	if err != nil {
		return "", fmt.Errorf("请求失败: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("读取响应失败: %w", err)
	}

	var r struct {
		ApiResponse
		Data *struct {
			AccessToken string `json:"access_token"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &r); err != nil || r.Data == nil || r.Data.AccessToken == "" {
		msg := r.Message
		if msg == "" {
			msg = "登录失败"
		}
		return "", fmt.Errorf("%s", msg)
	}
	return r.Data.AccessToken, nil
}

// StartupCheck 启动时的令牌检查：未配置令牌时提示游客模式或续期入口；
// 配置了则校验有效性并输出账号与 VIP 状态
func StartupCheck(ctx *shared.SiteContext) {
	token := curToken(ctx)
	if token == "" {
		if ctx.Username != "" && ctx.Password != "" {
			log.Printf("[mukaku] 未配置令牌：打开 /mukaku/renew 输入验证码即可自动登录获取（账号已配置）")
		} else {
			log.Printf("[mukaku] 未配置 MUKAKU_ACCESS_TOKEN，按游客模式抓取公开种子流")
		}
		return
	}

	// 令牌为 JWT（三段、含两个点），格式不符多为复制不完整，先行提示
	if strings.Count(token, ".") != 2 {
		log.Printf("[mukaku] 注意：令牌格式更像不完整的值（正常为三段式 JWT），请确认复制完整")
	}

	// 本地解析有效期，便于提前掌握续期时间
	if exp, ok := jwtExpiry(token); ok {
		if remain := time.Until(exp); remain > 0 {
			log.Printf("[mukaku] 令牌有效期至 %s（剩余 %d 天）",
				exp.In(ctx.CSTZone).Format("2006-01-02 15:04"), remainDays(remain))
		} else {
			log.Printf("[mukaku] 令牌已于 %s 过期",
				exp.In(ctx.CSTZone).Format("2006-01-02 15:04"))
		}
	}

	user, err := fetchUserInfo(ctx)
	if err != nil {
		log.Printf("[mukaku] access_token 校验未通过：%v（按游客模式抓取公开种子流）", err)
		logRenewHint(ctx)
		return
	}
	log.Printf("[mukaku] access_token 校验成功：账号 %s，VIP：%s", displayName(user), vipDesc(user))
}

// logRenewHint 输出续期入口提示
func logRenewHint(ctx *shared.SiteContext) {
	if ctx.Username != "" && ctx.Password != "" {
		log.Printf("[mukaku] 可打开 /mukaku/renew 输入验证码一键续期（无需重启）")
	} else {
		log.Printf("[mukaku] 配置 MUKAKU_USERNAME / MUKAKU_PASSWORD 后可在 /mukaku/renew 页面一键续期")
	}
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

// WatchToken 后台例行检查令牌有效期：临近过期时提醒续期，避免会员通道静默掉线
func WatchToken(ctx *shared.SiteContext) {
	ticker := time.NewTicker(6 * time.Hour)
	defer ticker.Stop()
	for range ticker.C {
		token := curToken(ctx)
		if token == "" {
			continue
		}
		exp, ok := jwtExpiry(token)
		if !ok {
			continue
		}
		remain := time.Until(exp)
		if remain > 0 && remain < expiryWarnWindow {
			log.Printf("[mukaku] 令牌将于 %s 过期（剩余 %d 天），请提前续期",
				exp.In(ctx.CSTZone).Format("2006-01-02 15:04"), remainDays(remain))
			logRenewHint(ctx)
		}
	}
}
