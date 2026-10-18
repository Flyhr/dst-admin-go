package update

import (
	"dst-admin-go/internal/pkg/utils/shellUtils"
	"dst-admin-go/internal/service/dstConfig"
	"log"
)

type WindowUpdate struct {
	dstConfig dstConfig.Config
}

func NewWindowUpdate(dstConfig dstConfig.Config) *WindowUpdate {
	return &WindowUpdate{
		dstConfig: dstConfig,
	}
}

func (u WindowUpdate) Update(clusterName string) error {
	unlock, err := beginUpdate()
	if err != nil {
		return err
	}
	defer unlock()

	config, err := u.dstConfig.GetDstConfig(clusterName)
	if err != nil {
		return err
	}
	updateCommand, err := WindowUpdateCommand(config)
	if err != nil {
		return err
	}

	dstInstallDir := config.Force_install_dir
	if config.Beta == 1 {
		dstInstallDir = dstInstallDir + "-beta"
	}
	setupPath, setupContent, setupHad := captureModsSetupFile(dstInstallDir)
	defer restoreModsSetupFile(setupPath, setupContent, setupHad)

	log.Println("正在更新游戏", "cluster: ", clusterName, "command: ", updateCommand)
	result, err := shellUtils.ExecuteCommandInWin(updateCommand)
	log.Println(result)
	return err
}
