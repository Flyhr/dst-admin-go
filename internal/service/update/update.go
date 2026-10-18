package update

import (
	"dst-admin-go/internal/pkg/utils/fileUtils"
	"dst-admin-go/internal/service/dstConfig"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

type Update interface {
	Update(clusterName string) error
}

// updateMutex 保证同一时间只有一个更新任务（手动/定时/重启流程）在运行 SteamCMD。
var updateMutex sync.Mutex

func beginUpdate() (func(), error) {
	if !updateMutex.TryLock() {
		return nil, fmt.Errorf("已有更新任务正在执行，请稍候再试")
	}
	return updateMutex.Unlock, nil
}

func EscapePath(path string) string {
	if runtime.GOOS == "windows" {
		return path
	}
	// 在这里添加需要转义的特殊字符
	escapedChars := []string{" ", "'", "(", ")"}
	for _, char := range escapedChars {
		path = strings.ReplaceAll(path, char, "\\"+char)
	}
	return path
}

func GetBaseUpdateCmd(cluster dstConfig.DstConfig) string {
	steamCmdPath := cluster.Steamcmd
	dstInstallDir := cluster.Force_install_dir
	if cluster.Beta == 1 {
		dstInstallDir = dstInstallDir + "-beta"
	}
	// 确保路径是跨平台兼容的
	dstInstallDir = filepath.Clean(EscapePath(dstInstallDir))
	steamCmdPath = filepath.Clean(steamCmdPath)

	// 构建基本命令：
	// - 使用 app_info_update 刷新元数据，避免 app_update 使用过期的应用信息；
	// - 不再默认加 validate：全量校验耗时长，还可能覆盖面板维护的 mods 配置。
	baseCmd := "+login anonymous +force_install_dir %s +app_info_update 1 +app_update 343050"
	if cluster.Beta == 1 {
		baseCmd += " -beta updatebeta"
	}
	baseCmd += " +quit"
	baseCmd = fmt.Sprintf(baseCmd, dstInstallDir)
	return baseCmd
}

func WindowUpdateCommand(cluster dstConfig.DstConfig) (string, error) {
	steamCmdPath := cluster.Steamcmd
	baseCmd := GetBaseUpdateCmd(cluster)

	var cmd string
	cmd = fmt.Sprintf("cd /d %s && Start steamcmd.exe %s", steamCmdPath, baseCmd)
	return cmd, nil
}

func LinuxUpdateCommand(cluster dstConfig.DstConfig) (string, error) {
	steamCmdPath := cluster.Steamcmd
	baseCmd := GetBaseUpdateCmd(cluster)
	var cmd string
	steamCmdScript := filepath.Join(steamCmdPath, "steamcmd.sh")
	if cluster.Bin == 86 {
		cmd = fmt.Sprintf("cd %s ; box86 ./linux32/steamcmd %s", steamCmdPath, baseCmd)
	} else {
		if fileUtils.Exists(steamCmdScript) {
			cmd = fmt.Sprintf("cd %s ; ./steamcmd.sh %s", steamCmdPath, baseCmd)
		} else {
			cmd = fmt.Sprintf("cd %s ; ./steamcmd %s", steamCmdPath, baseCmd)
		}
	}
	return cmd, nil
}

// captureModsSetupFile 备份 mods/dedicated_server_mods_setup.lua。
// SteamCMD 更新（尤其带 validate 时）可能覆盖它，导致面板维护的模组订阅丢失。
func captureModsSetupFile(dstInstallDir string) (path, content string, had bool) {
	path = filepath.Join(dstInstallDir, "mods", "dedicated_server_mods_setup.lua")
	data, err := os.ReadFile(path)
	if err != nil {
		return path, "", false
	}
	return path, string(data), true
}

func restoreModsSetupFile(path, content string, had bool) {
	if !had {
		return
	}
	current, err := os.ReadFile(path)
	if err == nil && string(current) == content {
		return
	}
	if err != nil {
		log.Println("读取 mods/dedicated_server_mods_setup.lua 失败，尝试恢复备份:", err)
	}
	if writeErr := fileUtils.WriterTXT(path, content); writeErr != nil {
		log.Println("恢复 mods/dedicated_server_mods_setup.lua 失败:", writeErr)
		return
	}
	log.Println("已恢复 mods/dedicated_server_mods_setup.lua（SteamCMD 更新覆盖了面板维护的模组订阅）")
}
