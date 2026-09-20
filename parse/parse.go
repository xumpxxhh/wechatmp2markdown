package parse

import (
	"bytes"
	"encoding/base64"
	"html"
	"io"
	"log"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/PuerkitoBio/goquery"
	xhtml "golang.org/x/net/html"
)

var (
	reDisplayNone   = regexp.MustCompile(`(?i)display\s*:\s*none`)
	reFontWeight    = regexp.MustCompile(`(?i)font-weight\s*:\s*([a-z0-9]+)`)
	reFontSize      = regexp.MustCompile(`(?i)font-size\s*:\s*([\d.]+)px`)
	reLetterSpacing = regexp.MustCompile(`(?i)letter-spacing\s*:\s*([\d.]+)px`)
	reColor         = regexp.MustCompile(`(?i)(?:^|;)\s*color\s*:\s*([^;]+)`)
	// 仅匹配完整章节标题行，避免「第一部分呈现了…」这类正文被误升为标题
	reSectionTitle = regexp.MustCompile(`^第[一二三四五六七八九十百千零〇两\d]+部分(小结|[：:].*)?$`)
	reCreateTime   = regexp.MustCompile(`var ct = "([0-9]+)"`)
	reMultiSpace   = regexp.MustCompile(`\s{2,}`)
	reHTTPInAttr   = regexp.MustCompile(`https?://[^\s"'<>]+`)
	zeroWidthReplacer = strings.NewReplacer(
		"\u200b", "", "\u200c", "", "\u200d", "", "\ufeff", "",
		"\u00a0", " ",
	)
)

func parseSection(s *goquery.Selection, imagePolicy ImagePolicy, lastPieceType PieceType) []Piece {
	var pieces []Piece
	var _lastPieceType PieceType = lastPieceType
	s.Contents().Each(func(i int, sc *goquery.Selection) {
		if sc.Nodes == nil || len(sc.Nodes) == 0 {
			return
		}
		node := sc.Nodes[0]
		if node.Type == xhtml.TextNode {
			text := normalizeVisibleText(node.Data)
			if text == "" {
				return
			}
			pieces = append(pieces, Piece{NORMAL_TEXT, text, nil})
			_lastPieceType = NORMAL_TEXT
			return
		}
		if node.Type != xhtml.ElementNode {
			return
		}

		if isHidden(sc) || isVisuallyEmpty(sc) {
			return
		}

		attr := make(map[string]string)
		switch {
		case sc.Is("br"):
			// 行内 br 当作软连接，避免中文词被切断；块级空行意图由段落结束 BR 表达
			if _lastPieceType == NORMAL_TEXT || _lastPieceType == BOLD_TEXT || _lastPieceType == LINK || _lastPieceType == ITALIC_TEXT {
				return
			}
			if _lastPieceType != BR && _lastPieceType != NULL {
				pieces = append(pieces, Piece{BR, nil, nil})
				_lastPieceType = BR
			}
		case sc.Is("a"):
			attr["href"], _ = sc.Attr("href")
			pieces = append(pieces, Piece{LINK, removeBrAndBlank(sc.Text()), attr})
			_lastPieceType = LINK
		case sc.Is("img"):
			resolveImageAttrs(sc, attr)
			if attr["src"] == "" {
				return
			}
			switch imagePolicy {
			case IMAGE_POLICY_URL:
				pieces = append(pieces, Piece{IMAGE, nil, attr})
			case IMAGE_POLICY_SAVE:
				image := fetchImgFile(attr["src"])
				if len(image) == 0 {
					return
				}
				pieces = append(pieces, Piece{IMAGE, image, attr})
			case IMAGE_POLICY_BASE64:
				fallthrough
			default:
				image := fetchImgFile(attr["src"])
				if len(image) == 0 {
					return
				}
				pieces = append(pieces, Piece{IMAGE_BASE64, img2base64(image), attr})
			}
			_lastPieceType = IMAGE
		case sc.Is("ol"):
			pieces = append(pieces, parseList(sc, O_LIST, imagePolicy)...)
			_lastPieceType = O_LIST
		case sc.Is("ul"):
			pieces = append(pieces, parseList(sc, U_LIST, imagePolicy)...)
			_lastPieceType = U_LIST
		case sc.Is("pre") || sc.Is("section.code-snippet__fix"):
			pieces = append(pieces, parsePre(sc)...)
			_lastPieceType = CODE_BLOCK
		case sc.Is("h1") || sc.Is("h2") || sc.Is("h3") || sc.Is("h4") || sc.Is("h5") || sc.Is("h6"):
			pieces = append(pieces, parseHeader(sc)...)
			_lastPieceType = HEADER
		case sc.Is("blockquote"):
			pieces = append(pieces, parseBlockQuote(sc, imagePolicy)...)
			_lastPieceType = BLOCK_QUOTES
		case sc.Is("strong") || sc.Is("b"):
			pieces = append(pieces, parseStrongOrStyledInline(sc, imagePolicy, true)...)
			if len(pieces) > 0 {
				_lastPieceType = pieces[len(pieces)-1].Type
			}
		case sc.Is("em") || sc.Is("i"):
			text := strings.TrimSpace(removeBrAndBlank(sc.Text()))
			if text != "" {
				pieces = append(pieces, Piece{ITALIC_TEXT, text, nil})
				_lastPieceType = ITALIC_TEXT
			}
		case sc.Is("table"):
			pieces = append(pieces, parseTable(sc)...)
			_lastPieceType = TABLE
		case sc.Is("p") || sc.Is("section") || sc.Is("figcaption") || sc.Is("figure"):
			blockPieces := parseBlock(sc, imagePolicy, _lastPieceType)
			pieces = append(pieces, blockPieces...)
			if len(pieces) > 0 {
				_lastPieceType = pieces[len(pieces)-1].Type
			}
		case sc.Is("span") || sc.Is("font"):
			spanPieces := parseSpan(sc, imagePolicy, _lastPieceType)
			pieces = append(pieces, spanPieces...)
			if len(pieces) > 0 {
				_lastPieceType = pieces[len(pieces)-1].Type
			}
		default:
			inner := parseSection(sc, imagePolicy, _lastPieceType)
			pieces = append(pieces, inner...)
			if len(pieces) > 0 {
				_lastPieceType = pieces[len(pieces)-1].Type
			}
		}
	})
	return pieces
}

func parseBlock(s *goquery.Selection, imagePolicy ImagePolicy, lastPieceType PieceType) []Piece {
	if isHidden(s) || isVisuallyEmpty(s) {
		return nil
	}

	hasMedia := s.Find("img").Length() > 0 || s.Find("table").Length() > 0

	text := strings.TrimSpace(removeBrAndBlank(s.Text()))
	style := combinedStyle(s)
	if !hasMedia {
		if level, ok := inferHeadingLevel(text, style, s); ok {
			attr := map[string]string{"level": strconv.Itoa(level)}
			pieces := []Piece{{HEADER, text, attr}}
			pieces = append(pieces, Piece{BR, nil, nil})
			return pieces
		}
	}

	inner := parseSection(s, imagePolicy, NULL)
	inner = compactPieces(inner)
	if len(inner) == 0 {
		return nil
	}

	if !hasMedia {
		if level, ok := inferHeadingFromPieces(inner, style); ok {
			title := strings.TrimSpace(plainTextFromPieces(inner))
			if title != "" {
				attr := map[string]string{"level": strconv.Itoa(level)}
				return []Piece{{HEADER, title, attr}, {BR, nil, nil}}
			}
		}
	}

	var pieces []Piece
	if lastPieceType != NULL && lastPieceType != BR && lastPieceType != HEADER {
		pieces = append(pieces, Piece{BR, nil, nil})
	}
	pieces = append(pieces, inner...)
	if len(pieces) > 0 && pieces[len(pieces)-1].Type != BR {
		pieces = append(pieces, Piece{BR, nil, nil})
	}
	return pieces
}

func parseSpan(s *goquery.Selection, imagePolicy ImagePolicy, lastPieceType PieceType) []Piece {
	if isHidden(s) || isVisuallyEmpty(s) {
		return nil
	}
	style := combinedStyle(s)
	text := strings.TrimSpace(removeBrAndBlank(s.Text()))

	// 短粗体着色标签行：作为三级标题
	if level, ok := inferHeadingLevel(text, style, s); ok {
		attr := map[string]string{"level": strconv.Itoa(level)}
		return []Piece{{HEADER, text, attr}, {BR, nil, nil}}
	}

	inner := parseSection(s, imagePolicy, lastPieceType)
	inner = compactPieces(inner)
	if len(inner) == 0 {
		return nil
	}

	if isBoldStyle(style) && isPlainTextPieces(inner) {
		joined := strings.TrimSpace(plainTextFromPieces(inner))
		if joined == "" {
			return nil
		}
		// 再次检查是否应升级为标题
		if level, ok := inferHeadingLevel(joined, style, s); ok {
			attr := map[string]string{"level": strconv.Itoa(level)}
			return []Piece{{HEADER, joined, attr}, {BR, nil, nil}}
		}
		return []Piece{{BOLD_TEXT, joined, nil}}
	}
	return inner
}

func parseStrongOrStyledInline(s *goquery.Selection, imagePolicy ImagePolicy, fromStrongTag bool) []Piece {
	text := strings.TrimSpace(removeBrAndBlank(s.Text()))
	if text == "" {
		return nil
	}
	style := combinedStyle(s)
	if level, ok := inferHeadingLevel(text, style, s); ok {
		attr := map[string]string{"level": strconv.Itoa(level)}
		return []Piece{{HEADER, text, attr}}
	}
	// strong 内若还有复杂子节点，尽量保留结构
	if s.Children().Length() > 0 {
		hasComplex := false
		s.Contents().Each(func(i int, sc *goquery.Selection) {
			if sc.Is("a") || sc.Is("img") || sc.Is("br") {
				hasComplex = true
			}
		})
		if hasComplex {
			inner := parseSection(s, imagePolicy, NULL)
			inner = compactPieces(inner)
			if isPlainTextPieces(inner) {
				joined := strings.TrimSpace(plainTextFromPieces(inner))
				if joined == "" {
					return nil
				}
				return []Piece{{BOLD_TEXT, joined, nil}}
			}
			return wrapPiecesAsBold(inner)
		}
	}
	_ = fromStrongTag
	return []Piece{{BOLD_TEXT, text, nil}}
}

func parseHeader(s *goquery.Selection) []Piece {
	var level int
	switch {
	case s.Is("h1"):
		level = 1
	case s.Is("h2"):
		level = 2
	case s.Is("h3"):
		level = 3
	case s.Is("h4"):
		level = 4
	case s.Is("h5"):
		level = 5
	case s.Is("h6"):
		level = 6
	}
	attr := map[string]string{"level": strconv.Itoa(level)}
	p := Piece{HEADER, strings.TrimSpace(removeBrAndBlank(s.Text())), attr}
	return []Piece{p}
}

func parsePre(s *goquery.Selection) []Piece {
	var codeRows []string
	s.Find("code").Each(func(i int, sc *goquery.Selection) {
		var codeLine string = ""
		sc.Contents().Each(func(i int, sc *goquery.Selection) {
			if goquery.NodeName(sc) == "br" {
				codeRows = append(codeRows, codeLine)
				codeLine = ""
			} else {
				codeLine += sc.Text()
			}
		})
		codeRows = append(codeRows, codeLine)
	})
	p := Piece{CODE_BLOCK, codeRows, nil}
	return []Piece{p}
}

func parseList(s *goquery.Selection, ptype PieceType, imagePolicy ImagePolicy) []Piece {
	var list []Piece
	s.Find("li").Each(func(i int, sc *goquery.Selection) {
		list = append(list, Piece{ptype, parseSection(sc, imagePolicy, ptype), nil})
	})
	return list
}

func parseBlockQuote(s *goquery.Selection, imagePolicy ImagePolicy) []Piece {
	var bq []Piece
	s.Contents().Each(func(i int, sc *goquery.Selection) {
		bq = append(bq, Piece{BLOCK_QUOTES, parseSection(sc, imagePolicy, BLOCK_QUOTES), nil})
	})
	bq = append(bq, Piece{BR, nil, nil})
	return bq
}

func parseTable(s *goquery.Selection) []Piece {
	var table []Piece
	htmlStr, _ := s.Html()
	table = append(table, Piece{TABLE, "<table>" + htmlStr + "</table>", map[string]string{"type": "native"}})
	return table
}

func parseMeta(s *goquery.Selection) []string {
	var res []string
	seen := map[string]bool{}
	add := func(t string) {
		t = strings.TrimSpace(removeBrAndBlank(t))
		if t == "" || seen[t] {
			return
		}
		seen[t] = true
		res = append(res, t)
	}

	// 优先结构化字段，避免把 profile 弹层整段 Text() 揉进来
	if copyright := strings.TrimSpace(s.Find("#copyright_logo").First().Text()); copyright != "" {
		add(copyright)
	}
	author := strings.TrimSpace(s.Find("#js_author_name_text").First().Text())
	if author == "" {
		author = strings.TrimSpace(s.Find("#js_author_name").First().Text())
	}
	if author != "" {
		add(author)
	}
	if name := strings.TrimSpace(s.Find("#js_name").First().Text()); name != "" {
		add(name)
	}
	if pub := strings.TrimSpace(s.Find("#publish_time").First().Text()); pub != "" {
		add(pub)
	}

	if len(res) > 0 {
		return res
	}

	// 回退：旧结构逐个子节点（仍跳过隐藏）
	s.Children().Each(func(i int, sc *goquery.Selection) {
		if isHidden(sc) {
			return
		}
		if sc.Is("#profileBt") {
			add(sc.Find("#js_name").Text())
			return
		}
		add(sc.Text())
	})
	return res
}

func ParseFromReader(r io.Reader, imagePolicy ImagePolicy) Article {
	var article Article
	doc, err := goquery.NewDocumentFromReader(r)
	if err != nil {
		log.Fatal(err)
	}
	var mainContent *goquery.Selection = doc.Find("#img-content")

	title := strings.TrimSpace(removeBrAndBlank(mainContent.Find("#activity-name").Text()))
	attr := map[string]string{"level": "1"}
	article.Title = Piece{HEADER, title, attr}

	meta := mainContent.Find("#meta_content")
	metastring := parseMeta(meta)
	article.Meta = metastring
	findstrs := reCreateTime.FindStringSubmatch(doc.Find("script").Text())
	if len(findstrs) > 1 {
		createTime := findstrs[1]
		timestamp, _ := strconv.Atoi(createTime)
		t := time.Unix(int64(timestamp), 0)
		article.Meta = append(article.Meta, t.Format("2006-01-02 15:04"))
	}

	tags := mainContent.Find("#js_tags").Text()
	tags = removeBrAndBlank(tags)
	article.Tags = tags

	content := mainContent.Find("#js_content")
	pieces := parseSection(content, imagePolicy, NULL)
	article.Content = compactPieces(pieces)

	return article
}

func ParseFromHTMLString(s string, imagePolicy ImagePolicy) Article {
	return ParseFromReader(strings.NewReader(s), imagePolicy)
}

func ParseFromHTMLFile(filepath string, imagePolicy ImagePolicy) Article {
	file, err := os.Open(filepath)
	if err != nil {
		panic(err)
	}
	defer file.Close()
	content, err2 := io.ReadAll(file)
	if err2 != nil {
		panic(err)
	}
	return ParseFromReader(bytes.NewReader(content), imagePolicy)
}

func ParseFromURL(url string, imagePolicy ImagePolicy) Article {
	imageFetchReferer = url
	defer func() { imageFetchReferer = "" }()
	req, err := http.NewRequest("GET", url, nil)

	if err != nil {
		log.Fatalf("new request %s error: %s", url, err.Error())
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/133.0.0.0 Safari/537.36 Edg/133.0.0.0")
	client := &http.Client{}
	res, err := client.Do(req)
	if err != nil {
		log.Fatalf("request to url %s error: %s", url, err.Error())
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		log.Fatalf("get from url %s error: %d %s", url, res.StatusCode, res.Status)
	}
	return ParseFromReader(res.Body, imagePolicy)
}

func removeBrAndBlank(s string) string {
	s = zeroWidthReplacer.Replace(s)
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	s = reMultiSpace.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}

func normalizeVisibleText(s string) string {
	s = zeroWidthReplacer.Replace(s)
	if strings.TrimSpace(s) == "" {
		return ""
	}
	// 保留单词间单空格，去掉纯排版换行
	s = strings.ReplaceAll(s, "\n", "")
	s = strings.ReplaceAll(s, "\r", "")
	return s
}

func isHidden(s *goquery.Selection) bool {
	style, _ := s.Attr("style")
	if reDisplayNone.MatchString(style) {
		return true
	}
	aria, _ := s.Attr("aria-hidden")
	return aria == "true"
}

func isVisuallyEmpty(s *goquery.Selection) bool {
	if s.Is("img") || s.Is("table") || s.Is("video") || s.Is("iframe") {
		return false
	}
	if s.Find("img").Length() > 0 || s.Find("table").Length() > 0 {
		return false
	}
	text := normalizeVisibleText(s.Text())
	return text == ""
}

func combinedStyle(s *goquery.Selection) string {
	// 只用自身 + 直接子代样式，避免整段正文因个别着色词被当成标题
	var parts []string
	style, _ := s.Attr("style")
	if style != "" {
		parts = append(parts, style)
	}
	s.Children().Each(func(i int, sc *goquery.Selection) {
		st, _ := sc.Attr("style")
		if st != "" {
			parts = append(parts, st)
		}
		sc.Children().Each(func(j int, gc *goquery.Selection) {
			gst, _ := gc.Attr("style")
			if gst != "" {
				parts = append(parts, gst)
			}
		})
	})
	return strings.Join(parts, ";")
}

func isBoldStyle(style string) bool {
	m := reFontWeight.FindStringSubmatch(style)
	if len(m) < 2 {
		return false
	}
	w := strings.ToLower(m[1])
	if w == "bold" || w == "bolder" {
		return true
	}
	if n, err := strconv.Atoi(w); err == nil && n >= 600 {
		return true
	}
	return false
}

func fontSizePx(style string) float64 {
	m := reFontSize.FindStringSubmatch(style)
	if len(m) < 2 {
		return 0
	}
	v, _ := strconv.ParseFloat(m[1], 64)
	return v
}

func letterSpacingPx(style string) float64 {
	m := reLetterSpacing.FindStringSubmatch(style)
	if len(m) < 2 {
		return 0
	}
	v, _ := strconv.ParseFloat(m[1], 64)
	return v
}

func hasEmphasisColor(style string) bool {
	m := reColor.FindStringSubmatch(style)
	if len(m) < 2 {
		return false
	}
	c := strings.ToLower(strings.TrimSpace(m[1]))
	// 常见正文黑/灰不算强调色
	if strings.Contains(c, "rgb(0") || strings.Contains(c, "#000") || strings.Contains(c, "#333") ||
		strings.Contains(c, "#666") || strings.Contains(c, "#999") || strings.Contains(c, "136, 136, 136") {
		return false
	}
	return strings.Contains(c, "rgb") || strings.HasPrefix(c, "#")
}

func runeLen(s string) int {
	return utf8.RuneCountInString(s)
}

func looksLikeBrokenHeadingFragment(text string) bool {
	if text == "" {
		return true
	}
	r, _ := utf8.DecodeRuneInString(text)
	switch r {
	case '—', '–', '-', '（', '(', '）', ')', '、', '，', ',', '：', ':':
		return true
	}
	// 明显未结束的半截标题
	if strings.HasSuffix(text, "—") || strings.HasSuffix(text, "–") {
		return true
	}
	return false
}

func inferHeadingLevel(text, style string, s *goquery.Selection) (int, bool) {
	text = strings.TrimSpace(text)
	if text == "" || looksLikeBrokenHeadingFragment(text) || isFooterNoise(text) {
		return 0, false
	}
	// 章节标题允许稍长；其它启发式仍限制长度
	if reSectionTitle.MatchString(text) && !strings.Contains(text, "。") && runeLen(text) <= 60 {
		return 2, true
	}
	if runeLen(text) > 40 {
		return 0, false
	}
	hasStrong := s != nil && (s.Find("strong").Length() > 0 || s.Is("strong") || s.Is("b"))
	bold := hasStrong || isBoldStyle(style)
	size := fontSizePx(style)
	spacing := letterSpacingPx(style)

	// 章节帽：短粗体 + 较大字号或明显字距，且像标题而非句子
	if bold && runeLen(text) <= 28 && (size >= 16 || spacing >= 2) {
		if strings.HasSuffix(text, "小结") ||
			(strings.Contains(text, "——") && !strings.Contains(text, "。")) {
			return 2, true
		}
		if (strings.Contains(text, "：") || strings.Contains(text, ":")) && !strings.Contains(text, "。") && runeLen(text) <= 16 {
			return 2, true
		}
	}
	// 小节标签：短、加粗、强调色
	if bold && runeLen(text) <= 16 && hasEmphasisColor(style) && !strings.Contains(text, "。") &&
		!strings.Contains(text, "，") {
		return 3, true
	}
	return 0, false
}

func isFooterNoise(text string) bool {
	if strings.Contains(text, "推荐阅读") || strings.Contains(text, "在看") ||
		strings.Contains(text, "赞") && strings.Contains(text, "分享") {
		return true
	}
	// 含 emoji / 装饰符号的运营条
	for _, r := range text {
		if r >= 0x1F300 && r <= 0x1FAFF {
			return true
		}
		if r == '👇' || r == '👆' || r == '👉' || r == '★' || r == '●' {
			return true
		}
	}
	return false
}

func inferHeadingFromPieces(pieces []Piece, style string) (int, bool) {
	text := strings.TrimSpace(plainTextFromPieces(pieces))
	if text == "" || looksLikeBrokenHeadingFragment(text) {
		return 0, false
	}
	onlyInline := true
	hasBold := false
	for _, p := range pieces {
		switch p.Type {
		case BR, NULL:
			continue
		case BOLD_TEXT, NORMAL_TEXT:
			if p.Type == BOLD_TEXT {
				hasBold = true
			}
		case HEADER:
			return 0, false
		default:
			onlyInline = false
		}
	}
	if !onlyInline {
		return 0, false
	}
	if reSectionTitle.MatchString(text) && !strings.Contains(text, "。") && runeLen(text) <= 60 {
		return 2, true
	}
	if hasBold && runeLen(text) <= 28 && !strings.Contains(text, "。") {
		if strings.HasSuffix(text, "小结") || strings.Contains(text, "——") {
			return 2, true
		}
	}
	if (hasBold || isBoldStyle(style)) && runeLen(text) <= 16 && hasEmphasisColor(style) &&
		!strings.Contains(text, "，") {
		return 3, true
	}
	return 0, false
}

func isCompleteHeadingText(text string) bool {
	text = strings.TrimSpace(text)
	if text == "" || looksLikeBrokenHeadingFragment(text) || strings.HasSuffix(text, "—") || strings.HasSuffix(text, "–") {
		return false
	}
	if reSectionTitle.MatchString(text) && !strings.Contains(text, "。") {
		return true
	}
	// 完整短标题标签（如「核心问题」「一句话总结」）
	if runeLen(text) >= 2 && runeLen(text) <= 16 && !strings.ContainsAny(text, "。！？；，") {
		return true
	}
	return false
}

func isInlineTextType(t PieceType) bool {
	return t == NORMAL_TEXT || t == BOLD_TEXT || t == ITALIC_TEXT
}

func compactPieces(pieces []Piece) []Piece {
	// 1) 去掉夹在行内文本碎片之间的 BR（保留段末段落分隔）
	stripped := make([]Piece, 0, len(pieces))
	for i, p := range pieces {
		if p.Type == BR {
			if i == 0 || i+1 >= len(pieces) {
				continue
			}
			prev, next := pieces[i-1], pieces[i+1]
			if shouldJoinAcrossBreak(prev, next) {
				continue
			}
		}
		stripped = append(stripped, p)
	}

	// 2) 合并相邻行内文本 / 标题碎片
	var out []Piece
	for _, p := range stripped {
		if p.Type == BOLD_TEXT {
			if s, ok := p.Val.(string); !ok || strings.TrimSpace(s) == "" {
				continue
			}
		}
		if p.Type == NORMAL_TEXT {
			if s, ok := p.Val.(string); !ok || s == "" {
				continue
			}
		}
		if p.Type == HEADER {
			if s, ok := p.Val.(string); !ok || strings.TrimSpace(s) == "" {
				continue
			}
		}
		if p.Type == BR {
			if len(out) == 0 || out[len(out)-1].Type == BR {
				continue
			}
			out = append(out, p)
			continue
		}
		if len(out) > 0 {
			if merged, ok := mergeInlinePieces(out[len(out)-1], p); ok {
				out[len(out)-1] = merged
				continue
			}
		}
		out = append(out, p)
	}
	for len(out) > 0 && out[len(out)-1].Type == BR {
		out = out[:len(out)-1]
	}
	return out
}

func shouldJoinAcrossBreak(prev, next Piece) bool {
	typesOK := (isInlineTextType(prev.Type) || prev.Type == HEADER) &&
		(isInlineTextType(next.Type) || next.Type == HEADER)
	if !typesOK {
		return false
	}
	ps, _ := prev.Val.(string)
	ns, _ := next.Val.(string)
	if ps == "" || ns == "" {
		return false
	}
	// 完整标题两侧都不与邻居粘连
	if prev.Type == HEADER && isCompleteHeadingText(ps) {
		return false
	}
	if next.Type == HEADER && isCompleteHeadingText(ns) {
		return false
	}
	if strings.HasSuffix(ps, "—") || strings.HasSuffix(ps, "–") ||
		strings.HasPrefix(ns, "—") || strings.HasPrefix(ns, "–") {
		return true
	}
	if prev.Type == BOLD_TEXT && next.Type == BOLD_TEXT && runeLen(ps) <= 36 && runeLen(ns) <= 36 {
		if isCompleteHeadingText(ps) || isCompleteHeadingText(ns) {
			return false
		}
		return true
	}
	if prev.Type == HEADER && !isCompleteHeadingText(ps) &&
		(next.Type == BOLD_TEXT || next.Type == NORMAL_TEXT) {
		return true
	}
	if prev.Type == BOLD_TEXT && next.Type == HEADER && !isCompleteHeadingText(ps) &&
		looksLikeBrokenHeadingFragment(ps) && runeLen(ns) <= 10 {
		return true
	}
	// 短续行：仅当上一截本身像半截标题（短/破折号）
	if looksLikeBrokenHeadingFragment(ps) && runeLen(ns) <= 8 && !strings.ContainsAny(ns, "。！？；") {
		pr, _ := utf8.DecodeLastRuneInString(ps)
		nr, _ := utf8.DecodeRuneInString(ns)
		if unicode.Is(unicode.Han, pr) && unicode.Is(unicode.Han, nr) &&
			(prev.Type == BOLD_TEXT || prev.Type == HEADER) {
			return true
		}
	}
	return false
}

func mergeInlinePieces(a, b Piece) (Piece, bool) {
	as, aOK := a.Val.(string)
	bs, bOK := b.Val.(string)
	if !aOK || !bOK || as == "" || bs == "" {
		return Piece{}, false
	}

	if a.Type == HEADER || b.Type == HEADER {
		if !shouldJoinAcrossBreak(a, b) {
			return Piece{}, false
		}
		joined := as + bs
		if reSectionTitle.MatchString(joined) && !strings.Contains(joined, "。") {
			return Piece{HEADER, joined, map[string]string{"level": "2"}}, true
		}
		return Piece{BOLD_TEXT, joined, nil}, true
	}

	if !isInlineTextType(a.Type) || !isInlineTextType(b.Type) {
		return Piece{}, false
	}

	outType := NORMAL_TEXT
	if a.Type == BOLD_TEXT && b.Type == BOLD_TEXT {
		outType = BOLD_TEXT
	} else if a.Type == BOLD_TEXT || b.Type == BOLD_TEXT {
		if looksLikeBrokenHeadingFragment(as) || looksLikeBrokenHeadingFragment(bs) ||
			strings.HasSuffix(as, "—") || strings.HasPrefix(bs, "—") {
			outType = BOLD_TEXT
		} else {
			return Piece{}, false
		}
	} else {
		// 纯正文相邻片段（去 BR 后）合并
		outType = NORMAL_TEXT
	}
	return Piece{Type: outType, Val: as + bs, Attrs: nil}, true
}

func isPlainTextPieces(pieces []Piece) bool {
	for _, p := range pieces {
		switch p.Type {
		case NORMAL_TEXT, BOLD_TEXT, ITALIC_TEXT, BR, NULL:
			continue
		default:
			return false
		}
	}
	return true
}

func plainTextFromPieces(pieces []Piece) string {
	var b strings.Builder
	for _, p := range pieces {
		switch p.Type {
		case NORMAL_TEXT, BOLD_TEXT, ITALIC_TEXT, LINK:
			if s, ok := p.Val.(string); ok {
				b.WriteString(s)
			}
		}
	}
	return b.String()
}

func wrapPiecesAsBold(pieces []Piece) []Piece {
	text := strings.TrimSpace(plainTextFromPieces(pieces))
	if text == "" {
		return nil
	}
	return []Piece{{BOLD_TEXT, text, nil}}
}

var imageFetchReferer string

func resolveImageAttrs(sc *goquery.Selection, attr map[string]string) {
	attr["alt"], _ = sc.Attr("alt")
	attr["title"], _ = sc.Attr("title")
	attr["src"] = pickImageURL(sc)
}

func pickImageURL(sc *goquery.Selection) string {
	dataSrc, _ := sc.Attr("data-src")
	src, _ := sc.Attr("src")
	for _, raw := range []string{dataSrc, src} {
		if u := normalizeImageURL(raw); u != "" {
			return u
		}
	}
	return ""
}

func normalizeImageURL(raw string) string {
	raw = strings.TrimSpace(html.UnescapeString(raw))
	if raw == "" {
		return ""
	}
	if strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://") {
		return raw
	}
	if m := reHTTPInAttr.FindString(raw); m != "" {
		return html.UnescapeString(m)
	}
	return ""
}

func fetchImgFile(url string) []byte {
	if url == "" {
		log.Println("skip image: url is empty")
		return nil
	}
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		log.Fatalf("get Image from url %s error: %s", url, err.Error())
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/133.0.0.0 Safari/537.36")
	if imageFetchReferer != "" {
		req.Header.Set("Referer", imageFetchReferer)
	} else {
		req.Header.Set("Referer", "https://mp.weixin.qq.com/")
	}
	client := &http.Client{Timeout: 60 * time.Second}
	res, err := client.Do(req)
	if err != nil {
		log.Fatalf("get Image from url %s error: %s", url, err.Error())
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		log.Fatalf("get Image from url %s error: %d %s", url, res.StatusCode, res.Status)
	}
	content, err := io.ReadAll(res.Body)
	if err != nil {
		log.Fatalf("read image Response error: %s", err.Error())
	}
	return content
}

func img2base64(content []byte) string {
	return base64.StdEncoding.EncodeToString(content)
}

type ImagePolicy int32

const (
	IMAGE_POLICY_URL ImagePolicy = iota
	IMAGE_POLICY_SAVE
	IMAGE_POLICY_BASE64
)

func ImageArgValue2ImagePolicy(val string) ImagePolicy {
	var imagePolicy ImagePolicy
	switch val {
	case "url":
		imagePolicy = IMAGE_POLICY_URL
	case "base64":
		imagePolicy = IMAGE_POLICY_BASE64
	case "save":
		fallthrough
	default:
		imagePolicy = IMAGE_POLICY_SAVE
	}
	return imagePolicy
}

// IsCJK reports whether r is a CJK character (exported for format post-process reuse via duplicate logic).
func IsCJK(r rune) bool {
	return unicode.Is(unicode.Han, r)
}
