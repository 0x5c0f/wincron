package main

import (
	"fmt"
	"os"
	"strings"

	"wincron/internal/config"
	"wincron/internal/notify"
)

// notifyCommand 提供通知渠道的终端侧管理能力。
//
//	wincron notify              列出渠道及就绪状态
//	wincron notify types        列出支持的渠道类型和必填字段
//	wincron notify test <名称>  立即向指定渠道发送一条测试消息
func notifyCommand(configPath string, args []string) {
	sub := "list"
	if len(args) > 0 {
		sub = args[0]
	}

	if sub == "types" {
		fmt.Println("支持的通知渠道类型（由 nikoksr/notify 提供实现）:")
		for _, t := range config.SupportedChannelTypes {
			fmt.Printf("  %-10s 必填: %s\n", t, strings.Join(config.ChannelRequiredFields(t), ", "))
		}
		fmt.Println("\n所有字符串字段都支持 ${ENV:变量名}，避免凭据明文写入 crontab.json。")
		return
	}

	cm := config.NewConfigManager(configPath)
	if err := cm.Load(); err != nil {
		fmt.Printf("加载配置失败: %v\n", err)
		os.Exit(1)
	}
	app := cm.GetAppConfig()
	notifier := notify.NewNotifier(app.NotifyChannels, app.NotifyTimeout, nil)

	switch sub {
	case "list":
		if len(app.NotifyChannels) == 0 {
			fmt.Println("当前没有配置任何通知渠道。请在 crontab.json 的 app.notify_channels 中添加。")
			fmt.Println("可用类型: wincron notify types")
			return
		}
		ready := make(map[string]bool)
		for _, st := range notifier.Statuses() {
			if st.Ready {
				ready[st.Name] = true
			}
		}
		fmt.Printf("共 %d 个通知渠道:\n", len(app.NotifyChannels))
		for _, ch := range app.NotifyChannels {
			state := "可用"
			if !ready[ch.Name] {
				state = "不可用"
			}
			fmt.Printf("  %-24s %-10s %s\n", ch.Name, ch.Type, state)
		}
		for _, p := range notifier.BuildErrors() {
			fmt.Printf("  错误: %s\n", p)
		}

	case "test":
		if len(args) < 2 {
			fmt.Println("用法: wincron notify test <渠道名称>")
			os.Exit(1)
		}
		name := args[1]
		fmt.Printf("正在向渠道 %q 发送测试通知...\n", name)
		if err := notifier.TestChannel(name); err != nil {
			fmt.Printf("测试失败: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("测试通知已发送成功。")

	default:
		fmt.Printf("未知子命令: %s\n", sub)
		fmt.Println("用法: wincron notify [list|types|test <渠道名称>]")
		os.Exit(1)
	}
}
