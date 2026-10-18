package update

import (
	"context"
	"dst-admin-go/internal/pkg/utils/cmdrun"
	"dst-admin-go/internal/pkg/utils/steamcmd"
	"dst-admin-go/internal/service/dstConfig"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	// 普通更新总预算（含重试）。
	updateCommandTimeout = 45 * time.Minute
	// 深度修复（validate）总预算（含重试）。
	repairCommandTimeout = 90 * time.Minute
	// 首次尝试失败后，剩余预算低于该值就不再重试，避免总耗时翻倍。
	minUpdateRetryBudget = 5 * time.Minute
)

type LinuxUpdate struct {
	dstConfig dstConfig.Config
}

func NewLinuxUpdate(dstConfig dstConfig.Config) *LinuxUpdate {
	return &LinuxUpdate{
		dstConfig: dstConfig,
	}
}

func (u LinuxUpdate) Update(clusterName string) error {
	return u.run(clusterName, false)
}

func (u LinuxUpdate) Repair(clusterName string) error {
	return u.run(clusterName, true)
}

func (u LinuxUpdate) run(clusterName string, deep bool) error {
	unlock, err := beginUpdate()
	if err != nil {
		return err
	}
	defer unlock()

	config, err := u.dstConfig.GetDstConfig(clusterName)
	if err != nil {
		return err
	}
	updateCommand, err := LinuxUpdateCommand(config, deep)
	if err != nil {
		return err
	}

	dstInstallDir := config.Force_install_dir
	if config.Beta == 1 {
		dstInstallDir = dstInstallDir + "-beta"
	}
	setupPath, setupContent, setupHad := captureModsSetupFile(dstInstallDir)
	defer restoreModsSetupFile(setupPath, setupContent, setupHad)

	action := "更新游戏"
	totalTimeout := updateCommandTimeout
	if deep {
		action = "深度修复游戏"
		totalTimeout = repairCommandTimeout
	}

	// 与版本检查共用 SteamCMD 锁，避免并发操作同一个 Steam 客户端。
	steamcmd.Lock()
	defer steamcmd.Unlock()

	deadline := time.Now().Add(totalTimeout)
	log.Println("正在"+action, "cluster: ", clusterName, "command: ", updateCommand)

	output, firstErr := cmdrun.Run(context.Background(), totalTimeout, "", "sh", "-c", updateCommand)
	if firstErr == nil {
		return nil
	}
	log.Println(action+"首次尝试失败: ", firstErr, "输出尾部: ", cmdrun.Tail(strings.TrimSpace(output), 600))

	remaining := time.Until(deadline)
	if remaining < minUpdateRetryBudget {
		return fmt.Errorf("%s（总预算 %s 已用尽，不再重试）", commandErrorText(firstErr, output), totalTimeout)
	}

	log.Println(action+"失败，清理 Steam 下载缓存后重试一次，剩余预算:", remaining.Round(time.Second))
	cleanupSteamDownloadCache(config)
	retryOutput, retryErr := cmdrun.Run(context.Background(), remaining, "", "sh", "-c", updateCommand)
	if retryErr != nil {
		return fmt.Errorf("首次尝试失败: %s；重试失败: %s", commandErrorText(firstErr, output), commandErrorText(retryErr, retryOutput))
	}
	return nil
}

// commandErrorText 把错误和输出尾部拼成适合页面展示的文本。
func commandErrorText(err error, output string) string {
	text := strings.TrimSpace(output)
	if text == "" {
		return err.Error()
	}
	return fmt.Sprintf("%s，输出尾部: %s", err.Error(), cmdrun.Tail(text, 600))
}

// cleanupSteamDownloadCache 清理下载中间状态，保留 appmanifest 以便更新后还能读取本地 buildid。
func cleanupSteamDownloadCache(config dstConfig.DstConfig) {
	dstInstallDir := config.Force_install_dir
	if config.Beta == 1 {
		dstInstallDir += "-beta"
	}
	paths := []string{
		filepath.Join(dstInstallDir, "steamapps", "downloading"),
		filepath.Join(dstInstallDir, "steamapps", "temp"),
	}
	for _, path := range paths {
		if err := os.RemoveAll(path); err != nil {
			log.Println("清理 Steam 下载缓存失败", "path: ", path, "error: ", err)
		}
	}
}
