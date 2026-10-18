package game

import (
	"dst-admin-go/internal/service/dstConfig"
	"dst-admin-go/internal/service/levelConfig"
	"fmt"
	"strings"
	"sync"
)

type WindowProcess struct {
	dstConfig        dstConfig.Config
	cli              *ClusterContainer
	levelConfigUtils *levelConfig.LevelConfigUtils
}

func NewWindowProcess(dstConfig *dstConfig.Config, levelConfigUtils *levelConfig.LevelConfigUtils) *WindowProcess {
	return &WindowProcess{
		dstConfig:        *dstConfig,
		cli:              NewClusterContainer(),
		levelConfigUtils: levelConfigUtils,
	}
}

func (p *WindowProcess) SessionName(clusterName, levelName string) string {
	return clusterName + "_" + levelName
}

func (p *WindowProcess) Start(clusterName, levelName string) error {
	config, err := p.dstConfig.GetDstConfig(clusterName)
	if err != nil {
		return err
	}
	go func() {
		p.cli.StartLevel(clusterName, levelName, config.Bin, config.Steamcmd, config.Force_install_dir, config.Ugc_directory, config.Persistent_storage_root, config.Conf_dir)
	}()

	return err
}

func (p *WindowProcess) Stop(clusterName, levelName string) error {
	p.cli.StopLevel(clusterName, levelName)
	return nil
}

func (p *WindowProcess) StartAll(clusterName string) error {
	if err := p.StopAll(clusterName); err != nil {
		return err
	}
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
			if launchErr := p.Start(clusterName, levelName); launchErr != nil {
				errMu.Lock()
				errs = append(errs, fmt.Sprintf("%s: %v", levelName, launchErr))
				errMu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	if len(errs) > 0 {
		return fmt.Errorf("启动世界失败: %s", strings.Join(errs, "; "))
	}
	return nil
}

func (p *WindowProcess) StopAll(clusterName string) error {
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
			if stopErr := p.Stop(clusterName, levelName); stopErr != nil {
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

func (p *WindowProcess) Status(clusterName, levelName string) (bool, error) {
	return p.cli.Status(clusterName, levelName), nil
}

func (p *WindowProcess) Command(clusterName, levelName, command string) error {
	p.cli.Send(clusterName, levelName, command)
	return nil
}

func (p *WindowProcess) PsAuxSpecified(clusterName, levelName string) DstPsAux {
	cpuUsage := p.cli.CpuUsage(clusterName, levelName)
	memUsage := p.cli.MemUsage(clusterName, levelName)
	dstPsAux := DstPsAux{}
	dstPsAux.RSS = fmt.Sprintf("%f", memUsage*1024)
	dstPsAux.CpuUage = fmt.Sprintf("%f", cpuUsage)
	return dstPsAux
}
