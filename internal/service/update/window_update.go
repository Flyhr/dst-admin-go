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
	return u.run(clusterName, false)
}

func (u WindowUpdate) Repair(clusterName string) error {
	return u.run(clusterName, true)
}

func (u WindowUpdate) run(clusterName string, deep bool) error {
	unlock, err := beginUpdate()
	if err != nil {
		return err
	}
	defer unlock()

	config, err := u.dstConfig.GetDstConfig(clusterName)
	if err != nil {
		return err
	}
	updateCommand, err := WindowUpdateCommand(config, deep)
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
	result, err := shellUtils.ExecuteCommandInWin(updateCommand)
	log.Println(result)
	return err
}
