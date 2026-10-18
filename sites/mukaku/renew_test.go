package mukaku

import (
	"strings"
	"testing"
)

// TestRenewPageSlots 校验续期页模板各占位各归其位：
// 出现参数错位或 % 未转义时 fmt 会留下 %! 痕迹，这里一并拦下
func TestRenewPageSlots(t *testing.T) {
	page := renewPage("STATUS-MARK", `<div>NOTICE-MARK</div>`, `<form>BODY-MARK</form>`)

	if !strings.Contains(page, `<p class="status">STATUS-MARK</p>`) {
		t.Fatalf("状态行未落在正确位置")
	}
	for _, mark := range []string{"NOTICE-MARK", "BODY-MARK"} {
		if !strings.Contains(page, mark) {
			t.Fatalf("缺少占位内容 %s", mark)
		}
	}
	if strings.Contains(page, "%!") || strings.Contains(page, "%%") {
		t.Fatalf("页面残留 fmt 转义痕迹")
	}
	if !strings.Contains(page, "width:100%;max-width:250px") {
		t.Fatalf("验证码图样式渲染不正确")
	}
}
