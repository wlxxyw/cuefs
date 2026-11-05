package main

import (
	"cuefs/internal/config"
	"fmt"
)

func main() {
	cfg, from, err := config.Load("C:/Users/wlx_x/AppData/Local/Temp/opencode/yamlfix.yaml")
	if err != nil {
		fmt.Println("err:", err)
		return
	}
	fmt.Println("from:", from, "ape→fmt:", cfg.Split.ApeConvertFormat, "ext:", cfg.Split.Extensions)
}
