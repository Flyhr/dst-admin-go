package schedule

import (
	"dst-admin-go/internal/service/backup"
	"dst-admin-go/internal/service/dstConfig"
	"dst-admin-go/internal/service/game"
	"dst-admin-go/internal/service/update"
	"log"
)

// Strategy 定时任务执行策略接口
type Strategy interface {
	Execute(clusterName, uuid string)
}

// StrategyContext 策略执行上下文，包含所有需要的依赖
type StrategyContext struct {
	GameProcess   game.Process
	BackupService *backup.BackupService
	UpdateService update.Update
	DstConfig     dstConfig.Config
}

// BackupStrategy 备份策略
type BackupStrategy struct {
	context *StrategyContext
}

func (s *BackupStrategy) Execute(clusterName, uuid string) {
	log.Printf("执行备份任务: cluster=%s, uuid=%s", clusterName, uuid)
	if s.context != nil && s.context.BackupService != nil {
		s.context.BackupService.CreateBackup(clusterName, "")
	}
}

// UpdateStrategy 更新策略
type UpdateStrategy struct {
	context *StrategyContext
}

// Execute 定时更新：先停服 → SteamCMD 更新 → 再启动，避免在游戏运行时改游戏文件。
func (s *UpdateStrategy) Execute(clusterName, uuid string) {
	log.Printf("执行更新任务: cluster=%s, uuid=%s", clusterName, uuid)
	if s.context == nil || s.context.UpdateService == nil {
		return
	}
	if s.context.GameProcess != nil {
		if err := s.context.GameProcess.StopAll(clusterName); err != nil {
			log.Printf("定时更新失败：停止世界失败: %v", err)
			return
		}
	}
	updateErr := s.context.UpdateService.Update(clusterName)
	if updateErr != nil {
		log.Printf("定时更新失败: %v", updateErr)
	}
	if s.context.GameProcess != nil {
		if err := s.context.GameProcess.StartAll(clusterName); err != nil {
			log.Printf("定时更新后启动世界失败: %v", err)
		}
	}
}

// StartStrategy 启动策略
type StartStrategy struct {
	context *StrategyContext
}

func (s *StartStrategy) Execute(clusterName, uuid string) {
	log.Printf("执行启动任务: cluster=%s, level=%s", clusterName, uuid)
	if s.context != nil && s.context.GameProcess != nil {
		s.context.GameProcess.Start(clusterName, uuid)
	}
}

// StopStrategy 停止策略
type StopStrategy struct {
	context *StrategyContext
}

func (s *StopStrategy) Execute(clusterName, uuid string) {
	log.Printf("执行停止任务: cluster=%s, level=%s", clusterName, uuid)
	if s.context != nil && s.context.GameProcess != nil {
		s.context.GameProcess.Stop(clusterName, uuid)
	}
}

// RestartStrategy 重启策略
type RestartStrategy struct {
	context *StrategyContext
}

func (s *RestartStrategy) Execute(clusterName, uuid string) {
	log.Printf("执行重启任务: cluster=%s, level=%s", clusterName, uuid)
	if s.context != nil && s.context.GameProcess != nil {
		s.context.GameProcess.Stop(clusterName, uuid)
		s.context.GameProcess.Start(clusterName, uuid)
	}
}

// RegenerateStrategy 重新生成世界策略
type RegenerateStrategy struct {
	context *StrategyContext
}

func (s *RegenerateStrategy) Execute(clusterName, uuid string) {
	log.Printf("执行重新生成世界任务: cluster=%s, level=%s", clusterName, uuid)
	if s.context != nil && s.context.GameProcess != nil {
		s.context.GameProcess.Command(clusterName, uuid, "c_regenerateworld()")
	}
}

// StartGameStrategy 启动游戏策略（所有世界）
type StartGameStrategy struct {
	context *StrategyContext
}

func (s *StartGameStrategy) Execute(clusterName, uuid string) {
	log.Printf("执行启动游戏任务: cluster=%s", clusterName)
	if s.context != nil && s.context.GameProcess != nil {
		s.context.GameProcess.StartAll(clusterName)
	}
}

// StopGameStrategy 停止游戏策略（所有世界）
type StopGameStrategy struct {
	context *StrategyContext
}

func (s *StopGameStrategy) Execute(clusterName, uuid string) {
	log.Printf("执行停止游戏任务: cluster=%s", clusterName)
	if s.context != nil && s.context.GameProcess != nil {
		s.context.GameProcess.StopAll(clusterName)
	}
}

// NoneStrategy 无操作策略
type NoneStrategy struct {
	context *StrategyContext
}

func (s *NoneStrategy) Execute(clusterName, uuid string) {
	log.Printf("执行无操作任务: cluster=%s, uuid=%s", clusterName, uuid)
}
