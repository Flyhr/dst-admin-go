package schedule

import (
	"dst-admin-go/internal/service/archive"
	"dst-admin-go/internal/service/backup"
	"dst-admin-go/internal/service/dstConfig"
	"dst-admin-go/internal/service/game"
	"dst-admin-go/internal/service/maintenance"
	"dst-admin-go/internal/service/mod"
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
	Archive       *archive.PathResolver
	ModService    *mod.ModService
}

// maintenanceActive 返回当前维护任务描述；为空表示没有更新/修复在执行。
func maintenanceActive() (bool, string) {
	snapshot := maintenance.Current()
	return snapshot.Active, snapshot.Kind
}

// skipInMaintenance 更新/修复期间跳过会启动世界的定时任务，避免游戏运行中读取半更新文件。
func skipInMaintenance(action string) bool {
	active, kind := maintenanceActive()
	if active {
		log.Printf("跳过定时任务[%s]：%s正在进行", action, kind)
		return true
	}
	return false
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

// Execute 定时更新：维护锁 → 停服 → 更新 → 核验 buildid → 成功后启动世界。
// 更新失败时不启动世界：SteamCMD 失败可能留下不完整文件，启动世界会扩大风险。
func (s *UpdateStrategy) Execute(clusterName, uuid string) {
	log.Printf("执行更新任务: cluster=%s, uuid=%s", clusterName, uuid)
	if s.context == nil || s.context.UpdateService == nil {
		return
	}
	if !maintenance.TryBegin("定时更新游戏") {
		log.Println("跳过定时更新：已有更新/修复任务正在执行")
		return
	}
	success := false
	restarted := false
	defer func() {
		maintenance.Finish(success, restarted)
	}()

	if s.context.GameProcess != nil {
		if err := s.context.GameProcess.StopAll(clusterName); err != nil {
			log.Printf("定时更新取消：停止世界失败: %v；本次不执行更新", err)
			return
		}
	}

	updateErr := s.context.UpdateService.Update(clusterName)
	// 更新无论成功与否都可能改动过本地文件，清空版本缓存让页面重新取数。
	if s.context.Archive != nil {
		s.context.Archive.InvalidateVersionCache(clusterName)
	}
	if updateErr != nil {
		log.Printf("定时更新失败: %v；世界保持停止状态，请人工检查版本后启动世界", updateErr)
		return
	}
	success = true

	if s.context.Archive != nil {
		if buildID, err := s.context.Archive.GetLocalDstBuildId(clusterName); err == nil {
			log.Printf("定时更新完成：buildid=%d", buildID)
		} else {
			log.Printf("定时更新后读取本地 buildid 失败: %v", err)
		}
	}

	if s.context.GameProcess != nil {
		if s.context.ModService != nil {
			if repairErr := s.context.ModService.RepairStaleUgcRecords(clusterName); repairErr != nil {
				log.Println("清理失效 UGC 记录失败:", repairErr)
			}
		}
		if err := s.context.GameProcess.StartAll(clusterName); err != nil {
			log.Printf("定时更新后启动世界失败: %v", err)
			return
		}
		restarted = true
		if s.context.ModService != nil {
			if trackErr := s.context.ModService.BeginInstallTracking(clusterName); trackErr != nil {
				log.Println("登记模组安装状态失败:", trackErr)
			}
		}
	}
}

// StartStrategy 启动策略
type StartStrategy struct {
	context *StrategyContext
}

func (s *StartStrategy) Execute(clusterName, uuid string) {
	log.Printf("执行启动任务: cluster=%s, level=%s", clusterName, uuid)
	if skipInMaintenance("启动世界") {
		return
	}
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
	if skipInMaintenance("重启世界") {
		return
	}
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
	if skipInMaintenance("重新生成世界") {
		return
	}
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
	if skipInMaintenance("启动所有世界") {
		return
	}
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
