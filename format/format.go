package format

import (
	"log"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/fengxxc/wechatmp2markdown/parse"
	"github.com/fengxxc/wechatmp2markdown/util"
)

const imageAssetsSubdir = "assets"

var (
	reMultiBlankLines = regexp.MustCompile(`\n{3,}`)
	reHardBreakJoin   = regexp.MustCompile("([\\p{Han}A-Za-z0-9）)」』》])(?:  \\n|\\n)([\\p{Han}A-Za-z0-9（(「『《—\\-])")
	reAdjacentBold    = regexp.MustCompile(`\*\*([^*]*)\*\*\*\*([^*]*)\*\*`)
	reBoldThenHeading = regexp.MustCompile(`\*\*([^*]+)\*\*#{1,6}\s+([^\n]+)`)
)

// Format format article
func Format(article parse.Article) (string, map[string][]byte) {
	var result string
	var titleMdStr string = formatTitle(article.Title)
	result += titleMdStr
	var metaMdStr string = formatMeta(article.Meta)
	result += metaMdStr
	var tagsMdStr string = formatTags(article.Tags)
	result += tagsMdStr
	var saveImageBytes map[string][]byte
	content, saveImageBytes := formatContent(article.Content, 0)
	result += content
	result = postProcessMarkdown(result)
	return result, saveImageBytes
}

// windows下, 文件名包含非法字符时, 用相似的Unicode字符进行替换; 长度超过255个字符时，保留前255个字符
func legalizationFilenameForWindows(name string) string {
	invalidChars := regexp.MustCompile(`[\\/:*?\"<>|]`)

	if invalidChars.MatchString(name) {
		name = strings.ReplaceAll(name, "<", "≺")
		name = strings.ReplaceAll(name, ">", "≻")
		name = strings.ReplaceAll(name, ":", "∶")
		name = strings.ReplaceAll(name, "\"", "“")
		name = strings.ReplaceAll(name, "/", "∕")
		name = strings.ReplaceAll(name, "\\", "∖")
		name = strings.ReplaceAll(name, "|", "∣")
		name = strings.ReplaceAll(name, "?", "?")
		name = strings.ReplaceAll(name, "*", "⁎")
	}

	if len(name) > 255 {
		name = name[:255]
	}

	return name
}

func legalizationFilenameForLinux(name string) string {
	invalidChars := regexp.MustCompile(`[\/]`)

	if invalidChars.MatchString(name) {
		name = strings.ReplaceAll(name, "/", "∕")
	}

	return name
}

// FormatAndSave fomat article and save to local file
func FormatAndSave(article parse.Article, filePath string) error {
	var basePath string
	var fileName string
	var isWin bool = runtime.GOOS == "windows"
	var isLinux bool = runtime.GOOS == "linux"
	var separator string
	if isWin {
		separator = "\\"
	} else {
		separator = "/"
	}
	if filePath == "" {
		filePath = "." + separator
	}
	if strings.HasPrefix(filePath, "./") || strings.HasPrefix(filePath, ".\\") {
		wd, _ := os.Getwd()
		filePath = strings.Replace(filePath, ".", wd, 1)
	}
	if strings.HasSuffix(filePath, ".md") {
		basePath = filePath[:strings.LastIndex(filePath, separator)]
		fileName = filePath
	} else {
		title := strings.TrimSpace(article.Title.Val.(string))
		if isWin {
			title = legalizationFilenameForWindows(title)
		} else if isLinux {
			title = legalizationFilenameForLinux(title)
		}
		basePath = filepath.Join(filePath, title)
		fileName = filepath.Join(basePath, title+".md")
	}

	if _, err := os.Stat(basePath); err != nil {
		if err := os.MkdirAll(basePath, 0755); err != nil {
			panic(err)
		}
	}

	var saveImageBytes map[string][]byte
	result, saveImageBytes := Format(article)
	if len(saveImageBytes) > 0 {
		assetsDir := filepath.Join(basePath, imageAssetsSubdir)
		if err := os.MkdirAll(assetsDir, 0755); err != nil {
			panic(err)
		}
		for imgRelPath, imgData := range saveImageBytes {
			imgfileName := filepath.Join(basePath, filepath.FromSlash(imgRelPath))
			if err := os.WriteFile(imgfileName, imgData, 0644); err != nil {
				log.Fatalf("can not save image file: %s\n err: %v", imgfileName, err)
			}
		}
	}
	return os.WriteFile(fileName, []byte(result), 0644)
}

func formatTitle(piece parse.Piece) string {
	var prefix string
	level, _ := strconv.Atoi(piece.Attrs["level"])
	if level <= 0 {
		level = 1
	}
	for i := 0; i < level; i++ {
		prefix += "#"
	}
	text := ""
	if s, ok := piece.Val.(string); ok {
		text = strings.TrimSpace(s)
	}
	return prefix + " " + text + "\n\n"
}

func formatMeta(meta []string) string {
	var parts []string
	for _, m := range meta {
		m = strings.TrimSpace(m)
		if m != "" {
			parts = append(parts, m)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, " ") + "\n\n"
}

func formatTags(tags string) string {
	tags = strings.TrimSpace(tags)
	if tags == "" {
		return ""
	}
	return tags + "\n\n"
}

func formatContent(pieces []parse.Piece, depth int) (string, map[string][]byte) {
	var contentMdStr string
	var base64Imgs []string
	var saveImageBytes map[string][]byte = make(map[string][]byte)
	for _, piece := range pieces {
		var pieceMdStr string
		var patchSaveImageBytes map[string][]byte
		switch piece.Type {
		case parse.HEADER:
			pieceMdStr = formatTitle(piece)
		case parse.LINK:
			pieceMdStr = formatLink(piece)
		case parse.NORMAL_TEXT:
			pieceMdStr = piece.Val.(string)
		case parse.BOLD_TEXT:
			text := strings.TrimSpace(piece.Val.(string))
			if text == "" {
				continue
			}
			pieceMdStr = "**" + text + "**"
		case parse.ITALIC_TEXT:
			pieceMdStr = "*" + piece.Val.(string) + "*"
		case parse.BOLD_ITALIC_TEXT:
			pieceMdStr = "***" + piece.Val.(string) + "***"
		case parse.IMAGE:
			if piece.Val == nil {
				pieceMdStr = formatImageInline(piece)
			} else {
				src := piece.Attrs["src"]
				imgExt := util.ParseImageExtFromSrc(src)
				if imgExt == "" {
					imgExt = "jpg"
				}
				var hashName string = util.MD5(piece.Val.([]byte)) + "." + imgExt
				relPath := imageAssetsSubdir + "/" + hashName
				saveImageBytes[relPath] = piece.Val.([]byte)
				pieceMdStr = formatImageFileReferInline(piece.Attrs["alt"], relPath)
			}
		case parse.IMAGE_BASE64:
			pieceMdStr = formatImageRefer(piece, len(base64Imgs))
			base64Imgs = append(base64Imgs, piece.Val.(string))
		case parse.TABLE:
			pieceMdStr = formatTable(piece)
		case parse.CODE_INLINE:
			// TODO
		case parse.CODE_BLOCK:
			pieceMdStr = formatCodeBlock(piece)
		case parse.BLOCK_QUOTES:
			pieceMdStr, patchSaveImageBytes = formatBlockQuote(piece, depth)
		case parse.O_LIST:
			pieceMdStr, patchSaveImageBytes = formatList(piece, depth)
		case parse.U_LIST:
			pieceMdStr, patchSaveImageBytes = formatList(piece, depth)
		case parse.HR:
			// TODO
		case parse.BR:
			pieceMdStr = "\n\n"
		case parse.NULL:
			continue
		}
		contentMdStr += pieceMdStr
		util.MergeMap(saveImageBytes, patchSaveImageBytes)
	}
	for i := 0; i < len(base64Imgs); i++ {
		contentMdStr += "\n[" + strconv.Itoa(i) + "]:" + "data:image/png;base64," + base64Imgs[i]
	}
	return contentMdStr, saveImageBytes
}

func postProcessMarkdown(s string) string {
	// 折叠相邻粗体：**a****b** → **ab**
	for i := 0; i < 8; i++ {
		next := reAdjacentBold.ReplaceAllString(s, "**$1$2**")
		if next == s {
			break
		}
		s = next
	}
	// 粗体半截后误接标题行：**…—**### 续 → **…—续**
	s = reBoldThenHeading.ReplaceAllStringFunc(s, func(m string) string {
		sub := reBoldThenHeading.FindStringSubmatch(m)
		if len(sub) < 3 {
			return m
		}
		left, right := sub[1], sub[2]
		if strings.HasSuffix(left, "—") || strings.HasPrefix(right, "—") || utf8.RuneCountInString(right) <= 10 {
			return "**" + left + right + "**"
		}
		return m
	})
	// 拼接误硬拆：汉字/字母 + 换行 + 汉字/字母
	for i := 0; i < 5; i++ {
		next := reHardBreakJoin.ReplaceAllString(s, "$1$2")
		if next == s {
			break
		}
		s = next
	}
	s = reMultiBlankLines.ReplaceAllString(s, "\n\n")
	s = strings.TrimSpace(s) + "\n"
	return s
}

func formatTable(piece parse.Piece) string {
	var tableMdStr string
	if piece.Attrs != nil && piece.Attrs["type"] == "native" {
		tableMdStr = piece.Val.(string)
	}
	return tableMdStr
}

func formatBlockQuote(piece parse.Piece, depth int) (string, map[string][]byte) {
	var bqMdString string
	var prefix string = ">"
	for i := 0; i < depth; i++ {
		prefix += ">"
	}
	prefix += " "
	var saveImageBytes map[string][]byte
	bqMdString, saveImageBytes = formatContent(piece.Val.([]parse.Piece), depth+1)
	return prefix + strings.TrimSpace(bqMdString) + "\n\n", saveImageBytes
}

func formatList(li parse.Piece, depth int) (string, map[string][]byte) {
	var listMdString string
	var prefix string
	for j := 0; j < depth; j++ {
		prefix += "    "
	}
	if li.Type == parse.U_LIST {
		prefix += "- "
	} else if li.Type == parse.O_LIST {
		prefix += strconv.Itoa(1) + ". "
	}
	var saveImageBytes map[string][]byte
	listMdString, saveImageBytes = formatContent(li.Val.([]parse.Piece), depth+1)
	return prefix + strings.TrimSpace(listMdString) + "\n\n", saveImageBytes
}

func formatCodeBlock(piece parse.Piece) string {
	var codeMdStr string
	codeMdStr += "```\n"
	codeRows := piece.Val.([]string)
	for _, row := range codeRows {
		codeMdStr += row + "\n"
	}
	codeMdStr += "```\n\n"
	return codeMdStr
}

func formatImageInline(piece parse.Piece) string {
	return "![" + piece.Attrs["alt"] + "](" + piece.Attrs["src"] + " \"" + piece.Attrs["title"] + "\")\n\n"
}

func formatImageFileReferInline(alt string, refName string) string {
	return "![" + alt + "](" + refName + ")\n\n"
}

func formatImageBase64Inline(piece parse.Piece) string {
	return "![" + piece.Attrs["alt"] + "](data:image/png;base64," + piece.Val.(string) + ")\n\n"
}

func formatImageRefer(piece parse.Piece, index int) string {
	return "![" + piece.Attrs["alt"] + "][" + strconv.Itoa(index) + "]\n\n"
}

func formatLink(piece parse.Piece) string {
	return "[" + piece.Val.(string) + "](" + piece.Attrs["href"] + ")"
}
