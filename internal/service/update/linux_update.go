package update

import (
	"context"
	"dst-admin-go/internal/pkg/utils/steamcmd"
	"dst-admin-go/internal/service/dstConfig"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	// 普通更新最长执行时间。
	updateCommandTimeout = 45 * time.Minute
	// 深度修复（validate）最长执行时间。
	repairCommandTimeout = 90 * time.Minute
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
	timeout := updateCommandTimeout
	if deep {
		action = "深度修复游戏"
		timeout = repairCommandTimeout
	}

	// 与版本检查共用 SteamCMD 锁，避免并发操作同一个 Steam 客户端。
	steamcmd.Lock()
	defer steamcmd.Unlock()

	log.Println("正在"+action, "cluster: ", clusterName, "command: ", updateCommand)
	_, err = runCommandWithTimeout(updateCommand, timeout)
	if err == nil {
		return nil
	}

	log.Println(action+"失败，清理 Steam 下载缓存后重试一次", "cluster: ", clusterName, "error: ", err)
	cleanupSteamDownloadCache(config)
	_, retryErr := runCommandWithTimeout(updateCommand, timeout)
	if retryErr != nil {
		return retryErr
	}
	return nil
}

// runCommandWithTimeout 执行 shell 命令并强制超时，返回输出尾部便于页面展示错误。
func runCommandWithTimeout(command string, timeout time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	output, err := cmd.CombinedOutput()
	text := string(output)
	if ctx.Err() == context.DeadlineExceeded {
		return text, fmt.Errorf("执行超时（限制 %s），输出尾部: %s", timeout, tailCommandOutput(text, 600))
	}
	if err != nil {
		return text, fmt.Errorf("执行失败: %v，输出尾部: %s", err, tailCommandOutput(text, 600))
	}
	return text, nil
}

func tailCommandOutput(text string, limit int) string {
	text = strings.TrimSpace(text)
	if len(text) <= limit {
		return text
	}
	return "..." + text[len(text)-limit:]
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
