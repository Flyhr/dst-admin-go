package game

import (
	"dst-admin-go/internal/pkg/utils/dstUtils"
	"dst-admin-go/internal/pkg/utils/shellUtils"
	"dst-admin-go/internal/service/dstConfig"
	"dst-admin-go/internal/service/levelConfig"
	"fmt"
	"log"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type LinuxProcess struct {
	dstConfig        dstConfig.Config
	levelConfigUtils *levelConfig.LevelConfigUtils
	mu               sync.Mutex // 保护启动/停止操作，防止并发执行
}

func NewLinuxProcess(dstConfig dstConfig.Config, levelConfigUtils *levelConfig.LevelConfigUtils) *LinuxProcess {
	return &LinuxProcess{
		dstConfig:        dstConfig,
		levelConfigUtils: levelConfigUtils,
	}
}

func (p *LinuxProcess) SessionName(clusterName, levelName string) string {
	return "DST_8level_" + levelName + "_" + clusterName
}

func (p *LinuxProcess) Start(clusterName, levelName string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	err := p.stop(clusterName, levelName)
	if err != nil {
		return err
	}
	return p.launchLevel(clusterName, levelName)
}

func (p *LinuxProcess) launchLevel(clusterName, levelName string) error {
	cluster, err := p.dstConfig.GetDstConfig(clusterName)
	if err != nil {
		return err
	}
	bin := cluster.Bin
	dstInstallDir := cluster.Force_install_dir
	if cluster.Beta == 1 {
		dstInstallDir = dstInstallDir + "-beta"
	}
	ugcDirectory := cluster.Ugc_directory
	persistent_storage_root := cluster.Persistent_storage_root
	conf_dir := cluster.Conf_dir
	var startCmd = ""

	dstInstallDir = dstUtils.EscapePath(dstInstallDir)
	log.Println(dstInstallDir)
	sessionName := p.SessionName(clusterName, levelName)
	if bin == 64 {
		startCmd = "cd " + dstInstallDir + "/bin64 ; screen -d -m -S \"" + sessionName + "\"  ./dontstarve_dedicated_server_nullrenderer_x64 -console -cluster " + clusterName + " -shard " + levelName
	} else if bin == 100 {
		startCmd = "cd " + dstInstallDir + "/bin64 ; screen -d -m -S \"" + sessionName + "\"  ./dontstarve_dedicated_server_nullrenderer_x64_luajit -console -cluster " + clusterName + " -shard " + levelName
	} else if bin == 86 {
		startCmd = "cd " + dstInstallDir + "/bin64 ; screen -d -m -S \"" + sessionName + "\" box86 ./dontstarve_dedicated_server_nullrenderer_x64 -console -cluster " + clusterName + " -shard " + levelName
	} else if bin == 2664 {
		startCmd = "cd " + dstInstallDir + "/bin64 ; screen -d -m -S \"" + sessionName + "\" box64 ./dontstarve_dedicated_server_nullrenderer_x64 -console -cluster " + clusterName + " -shard " + levelName
	} else {
		startCmd = "cd " + dstInstallDir + "/bin ; screen -d -m -S \"" + sessionName + "\"  ./dontstarve_dedicated_server_nullrenderer -console -cluster " + clusterName + " -shard " + levelName
	}

	if ugcDirectory != "" {
		startCmd += " -ugc_directory " + ugcDirectory
	}
	if persistent_storage_root != "" {
		startCmd += " -persistent_storage_root " + persistent_storage_root
	}
	if conf_dir != "" {
		startCmd += " -conf_dir " + conf_dir
	}
	startCmd += "  ;"
	log.Println("正在启动世界", "cluster: ", clusterName, "level: ", levelName, "command: ", startCmd)
	_, err = shellUtils.Shell(startCmd)
	return err
}

func (p *LinuxProcess) shutdownLevel(clusterName, levelName string) error {
	ok, err := p.Status(clusterName, levelName)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	return p.Command(clusterName, levelName, "c_shutdown(true)")
}

func (p *LinuxProcess) killLevel(clusterName, level string) error {

	if ok, err := p.Status(clusterName, level); err != nil || !ok {
		return nil
	}
	cmd := " ps -ef | grep -v grep | grep -v tail |grep '" + clusterName + "'|grep " + level + " |sed -n '1P'|awk '{print $2}' |xargs kill -9"
	log.Println("正在kill世界", "cluster: ", clusterName, "level: ", level, "command: ", cmd)
	_, err := shellUtils.Shell(cmd)
	return err
}

func (p *LinuxProcess) Stop(clusterName, levelName string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.stop(clusterName, levelName)
}

// stop 内部实现，不加锁，供 Start 等方法内部调用
func (p *LinuxProcess) stop(clusterName, levelName string) error {
	if err := p.shutdownLevel(clusterName, levelName); err != nil {
		return err
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		ok, err := p.Status(clusterName, levelName)
		if err != nil || !ok {
			return err
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("世界 %s 保存退出超时，请检查日志；进程未被强制结束", levelName)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func (p *LinuxProcess) StartAll(clusterName string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if err := p.stopAll(clusterName); err != nil {
		return err
	}
	config, err := p.levelConfigUtils.GetLevelConfig(clusterName)
	if err != nil {
		return err
	}
	var wg sync.WaitGroup
	var errMu sync.Mutex
	var errs []string
	wg.Add(len(config.LevelList))
	for i := range config.LevelList {
		go func(i int) {
			levelName := config.LevelList[i].File
			defer func() {
				if r := recover(); r != nil {
					errMu.Lock()
					errs = append(errs, fmt.Sprintf("%s: %v", levelName, r))
					errMu.Unlock()
				}
				wg.Done()
			}()
			if launchErr := p.launchLevel(clusterName, levelName); launchErr != nil {
				errMu.Lock()
				errs = append(errs, fmt.Sprintf("%s: %v", levelName, launchErr))
				errMu.Unlock()
			}
		}(i)
	}
	ClearScreen()
	wg.Wait()
	if len(errs) > 0 {
		return fmt.Errorf("启动世界失败: %s", strings.Join(errs, "; "))
	}
	return nil
}

func (p *LinuxProcess) StopAll(clusterName string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.stopAll(clusterName)
}

// stopAll 内部实现，不加锁，供 StartAll 等方法内部调用。
// 必须汇总每个分片的停止结果：任何一个世界没有真正退出都要返回错误，
// 否则更新/修复流程会在游戏仍在运行时改动游戏文件。
func (p *LinuxProcess) stopAll(clusterName string) error {
	config, err := p.levelConfigUtils.GetLevelConfig(clusterName)
	if err != nil {
		return fmt.Errorf("读取世界配置失败: %w", err)
	}
	var wg sync.WaitGroup
	var errMu sync.Mutex
	var errs []string
	wg.Add(len(config.LevelList))
	for i := range config.LevelList {
		go func(i int) {
			levelName := config.LevelList[i].File
			defer func() {
				if r := recover(); r != nil {
					errMu.Lock()
					errs = append(errs, fmt.Sprintf("%s: %v", levelName, r))
					errMu.Unlock()
				}
				wg.Done()
			}()
			if stopErr := p.stop(clusterName, levelName); stopErr != nil {
				errMu.Lock()
				errs = append(errs, fmt.Sprintf("%s: %v", levelName, stopErr))
				errMu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	if len(errs) > 0 {
		return fmt.Errorf("停止世界失败: %s", strings.Join(errs, "; "))
	}
	return nil
}

func (p *LinuxProcess) Status(clusterName, levelName string) (bool, error) {
	result, err := exec.Command("pgrep", "-af", "[d]ontstarve_dedicated_server_nullrenderer").Output()
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok && exit.ExitCode() == 1 {
			return false, nil
		}
		return false, err
	}
	for _, line := range strings.Split(string(result), "\n") {
		if matchesGameProcess(line, clusterName, levelName) {
			return true, nil
		}
	}
	return false, nil
}

func matchesGameProcess(line, clusterName, levelName string) bool {
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return false
	}
	// 启动命令可能是 `./dontstarve...`、`box86 ./dontstarve...`、`box64 ...`：
	// 可执行文件名不一定在第二个字段，因此先定位它。
	execIndex := -1
	for i := 1; i < len(fields); i++ {
		if strings.HasPrefix(filepath.Base(fields[i]), "dontstarve_dedicated_server_nullrenderer") {
			execIndex = i
			break
		}
	}
	if execIndex == -1 {
		return false
	}
	// 游戏进程只能直接启动，或由 box86/box64 唤起；
	// tmux/screen/bash 等包装进程的命令行里即使出现游戏路径，也不应当作游戏进程。
	for i := 1; i < execIndex; i++ {
		base := filepath.Base(fields[i])
		if !strings.HasPrefix(base, "box86") && !strings.HasPrefix(base, "box64") {
			return false
		}
	}
	cluster, shard := "", ""
	for i := 1; i+1 < len(fields); i++ {
		switch fields[i] {
		case "-cluster":
			cluster = fields[i+1]
		case "-shard":
			shard = fields[i+1]
		}
	}
	return cluster == clusterName && shard == levelName
}

func (p *LinuxProcess) Command(clusterName, levelName, command string) error {
	// Existing manually started servers can stay in their current tmux sessions.
	for _, session := range []string{p.SessionName(clusterName, levelName), "dst-" + strings.ToLower(levelName)} {
		startup, err := exec.Command("tmux", "display-message", "-p", "-t", session+":0.0", "#{pane_start_command}").Output()
		padded := " " + string(startup) + " "
		if err != nil || !strings.Contains(padded, " -cluster "+clusterName+" ") || !strings.Contains(padded, " -shard "+levelName+" ") {
			continue
		}
		if err := exec.Command("tmux", "send-keys", "-t", session+":0.0", "-l", "--", command).Run(); err != nil {
			return err
		}
		return exec.Command("tmux", "send-keys", "-t", session+":0.0", "Enter").Run()
	}
	// Do not route remote console commands through `sh -c`: panel commands often
	// contain Lua string quotes (for example c_give("cutgrass", 1)).  Shell
	// parsing strips or reinterprets those quotes before `screen` receives them,
	// causing DST to execute c_give(cutgrass, 1) or print(FOO) instead of the
	// intended Lua.  Pass argv directly so the command text is delivered exactly.
	return exec.Command(
		"screen",
		"-S", p.SessionName(clusterName, levelName),
		"-p", "0",
		"-X", "stuff",
		command+"\n",
	).Run()
}

func (p *LinuxProcess) PsAuxSpecified(clusterName, levelName string) DstPsAux {
	dstPsAux := DstPsAux{}
	cmd := "ps -aux | grep -v grep | grep -v tail | grep " + clusterName + "  | grep " + levelName + " | sed -n '2P' |awk '{print $3, $4, $5, $6}'"

	info, err := shellUtils.Shell(cmd)
	if err != nil {
		log.Println(cmd + " error: " + err.Error())
		return dstPsAux
	}
	if info == "" {
		return dstPsAux
	}

	arr := strings.Split(info, " ")
	dstPsAux.CpuUage = strings.Replace(arr[0], "\n", "", -1)
	dstPsAux.MemUage = strings.Replace(arr[1], "\n", "", -1)
	dstPsAux.VSZ = strings.Replace(arr[2], "\n", "", -1)
	dstPsAux.RSS = strings.Replace(arr[3], "\n", "", -1)

	return dstPsAux
}

const (
	// ClearScreenCmd 检查目前所有的screen作业，并删除已经无法使用的screen作业
	ClearScreenCmd = "screen -wipe "
)

func ClearScreen() bool {
	result, err := shellUtils.Shell(ClearScreenCmd)
	if err != nil {
		return false
	}
	res := strings.Split(result, "\n")[0]
	return res != ""
}
