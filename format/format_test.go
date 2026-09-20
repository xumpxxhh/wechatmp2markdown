package format_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/fengxxc/wechatmp2markdown/format"
	"github.com/fengxxc/wechatmp2markdown/parse"
)

func fixturePath(t *testing.T, name string) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Join(filepath.Dir(file), "..", "test", "testdata", name)
}

func TestAiCareerConversion(t *testing.T) {
	path := fixturePath(t, "ai_career.html")
	html, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	article := parse.ParseFromHTMLString(string(html), parse.IMAGE_POLICY_URL)
	md, _ := format.Format(article)

	if strings.Contains(md, "****") {
		t.Errorf("markdown contains empty bold ****")
	}
	if strings.Contains(md, "功能介绍") || strings.Contains(md, "微信号") {
		t.Errorf("meta leaked profile popup text")
	}
	if strings.Contains(md, "数\n据") || strings.Contains(md, "数  \n据") {
		t.Errorf("found mid-word hard break around 数据")
	}
	if strings.Contains(md, "管\n理") || strings.Contains(md, "管  \n理") {
		t.Errorf("found mid-word hard break around 管理")
	}

	if !strings.Contains(md, "## 第一部分") {
		t.Errorf("expected ## 第一部分 heading, got excerpt:\n%s", excerpt(md, "第一", 200))
	}
	if !strings.Contains(md, "## 第二部分") {
		t.Errorf("expected ## 第二部分 heading")
	}
	if !strings.Contains(md, "### 核心问题") && !strings.Contains(md, "**核心问题**") {
		t.Errorf("expected ### 核心问题 or bold fallback")
	}

	title := article.Title.Val.(string)
	if strings.HasPrefix(title, " ") || strings.TrimSpace(title) != title {
		t.Errorf("title not trimmed: %q", title)
	}
	for _, m := range article.Meta {
		if strings.Contains(m, "功能介绍") || strings.Contains(m, "微信号") {
			t.Errorf("meta item contains profile noise: %q", m)
		}
	}
}

func TestParseImagesInImageSection(t *testing.T) {
	html := `
<div id="img-content">
  <h1 id="activity-name">标题</h1>
  <div id="js_content">
    <section style="text-align:center;">
      <img class="rich_pages" data-src="" src="https://mmbiz.qpic.cn/mmbiz_jpg/foo/640?wx_fmt=jpeg" alt="图片" />
    </section>
  </div>
</div>`
	article := parse.ParseFromHTMLString(html, parse.IMAGE_POLICY_URL)
	var count int
	var walk func([]parse.Piece)
	walk = func(pieces []parse.Piece) {
		for _, p := range pieces {
			if p.Type == parse.IMAGE {
				count++
			}
			if v, ok := p.Val.([]parse.Piece); ok {
				walk(v)
			}
		}
	}
	walk(article.Content)
	if count != 1 {
		t.Fatalf("expected 1 image piece, got %d", count)
	}
}

func TestEmptyStrongAndLinkInline(t *testing.T) {
	html := `
<div id="img-content">
  <h1 id="activity-name">标题</h1>
  <div id="meta_content">
    <span id="copyright_logo">原创</span>
    <span id="js_author_name">作者甲</span>
    <span id="profileBt"><a id="js_name">公众号乙</a>
      <div class="profile_container" style="display:none;">
        <span>微信号</span><span>功能介绍</span>
      </div>
    </span>
    <em id="publish_time">2026-01-01</em>
  </div>
  <div id="js_content">
    <section><strong><span leaf=""><br/></span></strong></section>
    <section style="letter-spacing: 3px;font-size:17px;"><p><strong>第一部分：</strong></p><p><strong>三年影响——数据</strong></p></section>
    <section style="line-height:1.75em;"><span style="font-size:15px;color:rgb(2,30,170);font-weight:bold;">核心问题</span></section>
    <section><span>前文</span><a href="https://example.com">链接</a><span>后文</span></section>
    <section><span>常规认知任务</span><br/><span>（如</span><br/><span>记账、数据录入）</span></section>
  </div>
</div>`

	article := parse.ParseFromHTMLString(html, parse.IMAGE_POLICY_URL)
	md, _ := format.Format(article)

	if strings.Contains(md, "****") {
		t.Fatalf("empty strong should not produce ****\n%s", md)
	}
	if !strings.Contains(md, "## 第一部分") {
		t.Fatalf("expected section heading:\n%s", md)
	}
	if !strings.Contains(md, "### 核心问题") {
		t.Fatalf("expected level-3 heading:\n%s", md)
	}
	if !strings.Contains(md, "前文[链接](https://example.com)后文") {
		t.Fatalf("inline link should not break line:\n%s", md)
	}
	if strings.Contains(md, "功能介绍") {
		t.Fatalf("profile noise in output:\n%s", md)
	}
	metaJoined := strings.Join(article.Meta, " ")
	if !strings.Contains(metaJoined, "作者甲") || !strings.Contains(metaJoined, "公众号乙") {
		t.Fatalf("meta missing author/account: %#v", article.Meta)
	}
}

func excerpt(s, key string, n int) string {
	i := strings.Index(s, key)
	if i < 0 {
		if len(s) < n {
			return s
		}
		return s[:n]
	}
	start := i - 40
	if start < 0 {
		start = 0
	}
	end := i + n
	if end > len(s) {
		end = len(s)
	}
	return s[start:end]
}
