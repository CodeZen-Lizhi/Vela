package desktopinstall

import (
	"log"

	"github.com/egoist/mygo"
)

// Run 只运行原生安装确认与错误弹窗；确认后安装并打开目标，取消或完成后退出介质中的进程。
func Run(source, target string) error {
	mygo.App.WhenReady(func() {
		go func() {
			defer mygo.App.Quit()
			choice, err := mygo.Dialog.Message(mygo.MessageOptions{
				Type: mygo.MessageInfo, Title: "安装 Vela", Message: "将 Vela 安装到应用程序？",
				Detail:  "安装会正常退出旧版 Vela，网关将短暂停止。完成后自动打开新版，现有配置和记录保留。",
				Buttons: []string{"取消", "安装并打开"}, DefaultButton: 1, CancelButton: 0,
			})
			if err == nil && choice.Button == 1 {
				err = Install(source, target)
				if err == nil {
					err = Launch(target)
				}
			}
			if err != nil {
				log.Printf("DMG 安装: %v", err)
				if _, dialogErr := mygo.Dialog.Message(mygo.MessageOptions{
					Type: mygo.MessageError, Title: "Vela 安装未完成", Message: "安装未完成", Detail: err.Error(), Buttons: []string{"退出"},
				}); dialogErr != nil {
					log.Printf("显示安装错误失败: %v", dialogErr)
				}
			}
		}()
	})
	return mygo.App.Run()
}
