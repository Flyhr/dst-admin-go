package update

import (
	"dst-admin-go/internal/pkg/utils/shellUtils"
	"dst-admin-go/internal/service/dstConfig"
	"log"
	"os"
	"path/filepath"
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
	if deep {
		action = "深度修复游戏"
	}
	log.Println("正在"+action, "cluster: ", clusterName, "command: ", updateCommand)
	_, err = shellUtils.Shell(updateCommand)
	if err == nil {
		return nil
	}

	log.Println(action+"失败，清理 Steam 下载缓存后重试一次", "cluster: ", clusterName, "error: ", err)
	cleanupSteamDownloadCache(config)
	_, retryErr := shellUtils.Shell(updateCommand)
	if retryErr != nil {
		return retryErr
	}
	return nil
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
