// runQuick.go
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"

	"daxionglink/protocol" // 根据你的 module 路径进行调整
)

func runQuick(configFile string) {
	// 加载配置
	config, _ := LoadConfig(configFile)

	// 读取服务器列表
	servers, err := readLines(config.QServerFile)
	if err != nil || len(servers) == 0 {
		fmt.Println("无法读取", config.QServerFile, "或文件为空")
		return
	}

	server := servers[0] // 取最快的服务器（排序后第一行）

	// 从 TestURL 构造文件名：删除所有非法文件名字符
	re := regexp.MustCompile(`[^a-zA-Z0-9._-]`)
	fileBase := re.ReplaceAllString(config.TestURL, "")

	// 获取可执行文件的目录
	exePath, err := os.Executable()
	if err != nil {
		exePath = "." // 若失败则退回当前目录
	}
	exeDir := filepath.Dir(exePath)

	// 在 exe 目录下创建 config 子目录
	configDir := filepath.Join(exeDir, "config")
	if err := os.MkdirAll(configDir, os.ModePerm); err != nil {
		fmt.Println("创建 config 目录失败:", err)
		return
	}

	// 生成输出配置文件路径
	outFile := filepath.Join(configDir, fileBase+".json")

	// 生成 Xray/Sing-Box 配置
	if err := protocol.GenerateXrayConfig(server, config.Port, outFile); err != nil {
		fmt.Println("生成", outFile, "失败:", err)
		return
	}
	fmt.Println("✅ 已生成", outFile, "，正在启动", config.Core, "...")

	// 准备启动核心
	coreExe := "./" + config.Core
	if runtime.GOOS == "windows" {
		coreExe = ".\\" + config.Core + ".exe"
	}

	// 执行：mycore run -c <outFile>
	cmd := exec.Command(coreExe, "run", "-c", outFile)
	hideCommandWindow(cmd)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Println(coreExe, "运行出错:", err)
	}
}
