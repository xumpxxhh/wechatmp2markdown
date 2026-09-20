package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/fengxxc/wechatmp2markdown/format"
	"github.com/fengxxc/wechatmp2markdown/parse"
)

func main() {
	a := parse.ParseFromHTMLFile("test/test1.html", parse.IMAGE_POLICY_SAVE)
	if err := format.FormatAndSave(a, filepath.Join("tmp", "save_test3")); err != nil {
		panic(err)
	}
	filepath.Walk("tmp/save_test3", func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			fmt.Println(info.Size(), p)
		}
		return nil
	})
}
