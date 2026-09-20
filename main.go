package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/fengxxc/wechatmp2markdown/format"
	"github.com/fengxxc/wechatmp2markdown/parse"
	"github.com/fengxxc/wechatmp2markdown/server"
)

func main() {
	// test.Test1()
	// test.Test2()
	args := os.Args
	if len(args) < 2 {
		panic("not enough args")
	}
	args1 := args[1]

	if args1 == "server" {
		// server pattern
		port := "8964"
		if len(args) > 2 && args[2] != "" {
			port = args[2]
		}
		server.Start(":" + port)
		return
	}

	// CLI: url [filepath] [--image=...]
	// --image=save 	-is 保存图片，最终输出到文件夹（默认为此选项）
	// --image=url 		-iu 只保留图片链接
	// --image=base64 	-ib 保存图片，base64格式，在md文件中
	// --save=zip -sz 		最终打包输出到zip
	url := args1
	filename := "./"
	imageArgValue := "save"
	for i := 2; i < len(args); i++ {
		arg := args[i]
		if arg == "" {
			continue
		}
		if strings.HasPrefix(arg, "--image=") {
			imageArgValue = arg[len("--image="):]
			continue
		}
		if strings.HasPrefix(arg, "-i") {
			imageArgVal := arg[len("-i"):]
			switch imageArgVal {
			case "u":
				imageArgValue = "url"
			case "b":
				imageArgValue = "base64"
			case "s":
				fallthrough
			default:
				imageArgValue = "save"
			}
			continue
		}
		filename = arg
	}

	var imagePolicy parse.ImagePolicy = parse.ImageArgValue2ImagePolicy(imageArgValue)

	fmt.Printf("url: %s, filename: %s\n", url, filename)
	var articleStruct parse.Article = parse.ParseFromURL(url, imagePolicy)
	format.FormatAndSave(articleStruct, filename)
}
