package mukaku

import (
	"fmt"
	"html"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"web2rss/shared"

	"github.com/gorilla/mux"
)

// RegisterRenewRoutes 注册令牌一键续期页：
// GET 展示当前令牌状态与图形验证码，POST 提交验证码后用配置的账号自动登录换新令牌
func RegisterRenewRoutes(r *mux.Router, ctx *shared.SiteContext) {
	r.HandleFunc("/mukaku/renew", func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if req.Method == http.MethodPost {
			handleRenewSubmit(ctx, w, req)
			return
		}
		renderRenewForm(ctx, w, "", "")
	}).Methods(http.MethodGet, http.MethodPost)
}

// tokenStatusLine 当前令牌状态描述
func tokenStatusLine(ctx *shared.SiteContext) string {
	token := curToken(ctx)
	if token == "" {
		return "当前令牌：未配置"
	}
	if exp, ok := jwtExpiry(token); ok {
		if remain := time.Until(exp); remain > 0 {
			return fmt.Sprintf("当前令牌有效期至 %s（剩余 %d 天）",
				exp.In(ctx.CSTZone).Format("2006-01-02 15:04"), remainDays(remain))
		}
		return "当前令牌已于 " + exp.In(ctx.CSTZone).Format("2006-01-02 15:04") + " 过期"
	}
	return "当前令牌：已配置（未标注有效期）"
}

// renderRenewForm 渲染续期表单（含验证码）
func renderRenewForm(ctx *shared.SiteContext, w http.ResponseWriter, notice, noticeClass string) {
	form := ""
	if ctx.Username == "" || ctx.Password == "" {
		form = `<p class="hint">未配置账号密码，无法在页面续期。<br>容器启动时添加：<br>` +
			`<code>-e MUKAKU_USERNAME=账号</code><br><code>-e MUKAKU_PASSWORD=密码</code><br>重启后刷新本页即可一键续期。</p>`
	} else {
		img, key, err := fetchCaptcha(ctx)
		if err != nil {
			notice, noticeClass = "验证码获取失败："+err.Error()+"，请刷新重试", "err"
		} else {
			form = fmt.Sprintf(`<form method="post" action="/mukaku/renew">
  <img src="%s" alt="验证码" title="看不清？点击刷新" onclick="location.reload()">
  <input name="code" placeholder="输入图中验证码" autocomplete="off" autofocus required>
  <input type="hidden" name="key" value="%s">
  <button type="submit">一键续期</button>
</form>
<p class="hint">提交后自动用已配置的账号密码登录并更新令牌，立即生效、无需重启。</p>`,
				html.EscapeString(img), html.EscapeString(key))
		}
	}

	noticeHTML := ""
	if notice != "" {
		noticeHTML = fmt.Sprintf(`<div class="notice %s">%s</div>`, noticeClass, html.EscapeString(notice))
	}
	w.Write([]byte(renewPage(tokenStatusLine(ctx), noticeHTML, form)))
}

// handleRenewSubmit 处理续期表单提交
func handleRenewSubmit(ctx *shared.SiteContext, w http.ResponseWriter, req *http.Request) {
	code := strings.TrimSpace(req.FormValue("code"))
	key := strings.TrimSpace(req.FormValue("key"))

	if ctx.Username == "" || ctx.Password == "" {
		renderRenewForm(ctx, w, "未配置账号密码，无法续期", "err")
		return
	}
	if code == "" || key == "" {
		renderRenewForm(ctx, w, "请填写验证码后提交", "err")
		return
	}

	token, err := loginWithCaptcha(ctx, code, key)
	if err != nil {
		log.Printf("[mukaku] 页面续期失败：%v", err)
		renderRenewForm(ctx, w, "续期失败："+err.Error(), "err")
		return
	}

	adoptToken(token)

	// 登录成功后顺带确认账号与 VIP 状态
	user, uerr := fetchUserInfo(ctx)
	name, vip := "未知账号", "未知"
	if uerr == nil {
		name, vip = displayName(user), vipDesc(user)
	}
	expDesc := "未知"
	if exp, ok := jwtExpiry(token); ok {
		expDesc = exp.In(ctx.CSTZone).Format("2006-01-02 15:04")
	}
	log.Printf("[mukaku] 页面续期成功：账号 %s，VIP：%s，令牌有效期至 %s", name, vip, expDesc)

	persistDesc := "已保存，容器重启后继续有效"
	if _, err := os.Stat(tokenFile); err != nil {
		persistDesc = "未保存到文件：容器重建后需重新续期（可挂载 /data 卷解决）"
	}

	noticeHTML := fmt.Sprintf(`<div class="notice ok">续期成功：账号 %s，VIP：%s</div>`,
		html.EscapeString(name), html.EscapeString(vip))
	extra := fmt.Sprintf(`<p class="hint">新令牌有效期至 <b>%s</b>；%s。<br>服务已立即启用新令牌，无需重启。</p>`,
		html.EscapeString(expDesc), persistDesc)
	w.Write([]byte(renewPage(tokenStatusLine(ctx), noticeHTML, extra)))
}

// renewPage 组装续期页面 HTML
func renewPage(status, noticeHTML, bodyHTML string) string {
	return fmt.Sprintf(`<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>web2rss · mukaku 令牌续期</title>
<style>
  body{margin:0;padding:24px;background:#14161a;color:#e8eaed;
       font:15px/1.6 system-ui,-apple-system,"Segoe UI","PingFang SC","Microsoft YaHei",sans-serif;
       display:flex;justify-content:center}
  .card{width:100%%;max-width:420px;background:#1e2126;border-radius:12px;padding:24px;
        box-shadow:0 4px 24px rgba(0,0,0,.4);align-self:flex-start}
  h1{font-size:18px;margin:0 0 6px}
  .status{color:#9aa0a6;font-size:13px;margin:0 0 16px}
  .notice{padding:10px 12px;border-radius:8px;font-size:14px;margin-bottom:14px;word-break:break-all}
  .ok{background:#1b3a2a;color:#7ee2a8}
  .err{background:#3a1b1b;color:#f28b82}
  form{display:flex;flex-direction:column;gap:12px}
  img{cursor:pointer;border-radius:8px;background:#fff;width:130px;height:44px;object-fit:cover}
  input{height:42px;border-radius:8px;border:1px solid #3c4043;background:#14161a;color:#e8eaed;
        padding:0 12px;font-size:15px;outline:none}
  input:focus{border-color:#8ab4f8}
  button{height:44px;border:0;border-radius:8px;background:#8ab4f8;color:#14161a;
         font-size:15px;font-weight:600;cursor:pointer}
  button:hover{background:#aecbfa}
  .hint{color:#9aa0a6;font-size:13px;margin:14px 0 0}
  code{background:#2a2d33;padding:2px 6px;border-radius:4px;font-size:12px}
</style>
</head>
<body>
<div class="card">
  <h1>mukaku 令牌续期</h1>
  <p class="status">%s</p>
  %s
  %s
</div>
</body>
</html>`, status, noticeHTML, bodyHTML)
}
