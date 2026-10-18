package handler

import (
	"dst-admin-go/internal/pkg/context"
	"dst-admin-go/internal/pkg/response"
	"dst-admin-go/internal/service/archive"
	"dst-admin-go/internal/service/game"
	"dst-admin-go/internal/service/levelConfig"
	"dst-admin-go/internal/service/maintenance"
	"dst-admin-go/internal/service/mod"
	"dst-admin-go/internal/service/update"
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

const (
	// updateBusyCode 表示已有更新/修复任务在执行（与“执行失败”区分，调用方不能据此重启世界）。
	updateBusyCode = 409
)

type UpdateHandler struct {
	updateService    update.Update
	resolver         *archive.PathResolver
	gameProcess      game.Process
	levelConfigUtils *levelConfig.LevelConfigUtils
	modService       *mod.ModService
}

func NewUpdateHandler(
	update update.Update,
	resolver *archive.PathResolver,
	gameProcess game.Process,
	levelConfigUtils *levelConfig.LevelConfigUtils,
	modService *mod.ModService,
) *UpdateHandler {
	return &UpdateHandler{
		updateService:    update,
		resolver:         resolver,
		gameProcess:      gameProcess,
		levelConfigUtils: levelConfigUtils,
		modService:       modService,
	}
}

func (h *UpdateHandler) RegisterRoute(router *gin.RouterGroup) {
	router.GET("/api/game/update", h.Update)
	router.GET("/api/game/repair", h.Repair)
	router.GET("/api/game/version", h.Version)
	router.GET("/api/game/update/status", h.Status)
}

// Update 更新游戏。
// @Summary 更新游戏
// @Router /api/game/update [get]
func (h *UpdateHandler) Update(ctx *gin.Context) {
	h.run(ctx, false)
}

// Repair 深度修复游戏（SteamCMD validate 全量校验）。
// @Summary 深度修复游戏
// @Description 停止世界后使用 SteamCMD validate 全量校验/修复游戏文件，然后核验 buildid
// @Router /api/game/repair [get]
func (h *UpdateHandler) Repair(ctx *gin.Context) {
	h.run(ctx, true)
}

// Version 查询/强制检查游戏版本。
// GET /api/game/version           使用缓存（页面加载）
// GET /api/game/version?refresh=1 强制用 SteamCMD 做一次真实检查（手动刷新/更新前）
func (h *UpdateHandler) Version(ctx *gin.Context) {
	clusterName := context.GetClusterName(ctx)
	force := ctx.Query("refresh") == "1" || strings.EqualFold(ctx.Query("refresh"), "true")

	var (
		info archive.LatestDstVersionInfo
		err  error
	)
	if force {
		info, err = h.resolver.GetLatestDstVersionInfoForce(clusterName)
	} else {
		info, err = h.resolver.GetLatestDstVersionInfo(clusterName)
	}
	if err != nil {
		log.Println("检查游戏版本失败:", err)
		ctx.JSON(http.StatusOK, response.Response{
			Code: 500,
			Msg:  "检查游戏版本失败: " + err.Error(),
			Data: nil,
		})
		return
	}

	localBuildID, localErr := h.resolver.GetLocalDstBuildId(clusterName)
	if localErr != nil {
		log.Println("读取本地 buildid 失败:", localErr)
	}
	data := map[string]interface{}{
		"lastVersion":      info.Version,
		"versionCheckedAt": info.CheckedAt,
		"versionSource":    info.Source,
	}
	if localErr == nil {
		data["version"] = localBuildID
	} else {
		data["version"] = int64(0)
	}

	ctx.JSON(http.StatusOK, response.Response{
		Code: 200,
		Msg:  "get game version success",
		Data: data,
	})
}

// Status 查询当前是否有更新/修复任务在执行，以及上一次执行结果。
// GET /api/game/update/status
func (h *UpdateHandler) Status(ctx *gin.Context) {
	clusterName := context.GetClusterName(ctx)
	snapshot := maintenance.Current()
	data := map[string]interface{}{
		"updating":       snapshot.Active,
		"kind":           snapshot.Kind,
		"startedAt":      snapshot.StartedAt,
		"lastFinishedAt": snapshot.LastFinishedAt,
		"lastSuccess":    snapshot.LastSuccess,
		"lastRestarted":  snapshot.LastRestarted,
	}
	if buildID, err := h.resolver.GetLocalDstBuildId(clusterName); err == nil {
		data["version"] = buildID
	}
	ctx.JSON(http.StatusOK, response.Response{
		Code: 200,
		Msg:  "get update status success",
		Data: data,
	})
}

// run 执行一次更新/修复。
//
// 流程在服务端完成：维护锁 → 停服 → 更新/修复 → 核验 buildid → 成功后启动世界。
// 失败时不启动世界：SteamCMD 失败可能留下不完整文件，启动世界反而扩大风险。
func (h *UpdateHandler) run(ctx *gin.Context, deep bool) {
	clusterName := context.GetClusterName(ctx)
	action := "更新游戏"
	if deep {
		action = "深度修复游戏"
	}

	if !maintenance.TryBegin(action) {
		log.Println(action + "被拒绝：已有更新/修复任务正在执行")
		ctx.JSON(http.StatusOK, response.Response{
			Code: updateBusyCode,
			Msg:  "已有更新/修复任务正在执行，请稍后再试",
		})
		return
	}

	success := false
	restarted := false
	defer func() {
		maintenance.Finish(success, restarted)
	}()

	// 1. 服务端统一停服：不再依赖前端先调用停服接口。
	if h.gameProcess != nil {
		if stopErr := h.gameProcess.StopAll(clusterName); stopErr != nil {
			log.Println(action+"已取消：停止世界失败: ", stopErr)
			ctx.JSON(http.StatusOK, response.Response{
				Code: 500,
				Msg:  action + "已取消：停止世界失败: " + stopErr.Error() + "；请检查世界状态后手动启动",
			})
			return
		}
	}

	// 2. 复核对所有分片确实已停止，避免在游戏运行时改文件。
	if err := h.ensureWorldsStopped(clusterName); err != nil {
		log.Println(action+"已取消: ", err)
		ctx.JSON(http.StatusOK, response.Response{
			Code: 500,
			Msg:  action + "已取消: " + err.Error() + "；世界保持停止状态",
		})
		return
	}

	beforeBuildID, _ := h.resolver.GetLocalDstBuildId(clusterName)

	var updateErr error
	if deep {
		updateErr = h.updateService.Repair(clusterName)
	} else {
		updateErr = h.updateService.Update(clusterName)
	}

	// 更新/修复无论成功与否都可能改动过本地文件，清空版本缓存让下一次检查重新取数。
	h.resolver.InvalidateVersionCache(clusterName)

	if updateErr != nil {
		log.Println(action+"失败: ", updateErr)
		ctx.JSON(http.StatusOK, response.Response{
			Code: 500,
			Msg:  actionErrorMessage(action, updateErr) + "；世界保持停止状态，请先检查游戏版本再手动启动",
			Data: map[string]interface{}{
				"buildId":       beforeBuildID,
				"beforeBuildId": beforeBuildID,
				"restarted":     false,
			},
		})
		return
	}

	// 3. 核验 buildid：不能只看 SteamCMD 退出码。
	buildID, verifyErr := h.resolver.GetLocalDstBuildId(clusterName)
	if verifyErr != nil {
		log.Println(action + "后读取本地 buildid 失败: " + verifyErr.Error())
		ctx.JSON(http.StatusOK, response.Response{
			Code: 500,
			Msg:  action + "命令已执行，但无法读取本地 buildid 进行核验: " + verifyErr.Error() + "；世界保持停止状态，请检查后手动启动",
			Data: map[string]interface{}{
				"buildId":       beforeBuildID,
				"beforeBuildId": beforeBuildID,
				"restarted":     false,
			},
		})
		return
	}
	if verifyBuildErr := h.verifyUpdatedBuildID(clusterName, deep, beforeBuildID, buildID); verifyBuildErr != nil {
		log.Println(action + "后 buildid 核验失败: " + verifyBuildErr.Error())
		ctx.JSON(http.StatusOK, response.Response{
			Code: 500,
			Msg:  action + "后 buildid 核验失败: " + verifyBuildErr.Error() + "；世界保持停止状态，请检查游戏文件后手动启动",
			Data: map[string]interface{}{
				"buildId":       buildID,
				"beforeBuildId": beforeBuildID,
				"restarted":     false,
			},
		})
		return
	}

	success = true

	// 4. 更新/修复成功后再启动世界。
	if h.gameProcess != nil {
		if startErr := h.startWorlds(clusterName); startErr != nil {
			log.Println(action + "已完成，但启动世界失败: " + startErr.Error())
			ctx.JSON(http.StatusOK, response.Response{
				Code: 500,
				Msg:  action + "已完成（buildid " + fmt.Sprintf("%d", buildID) + "），但启动世界失败: " + startErr.Error() + "；请手动启动世界",
				Data: map[string]interface{}{
					"buildId":       buildID,
					"beforeBuildId": beforeBuildID,
					"restarted":     false,
				},
			})
			return
		}
		restarted = true
	}

	ctx.JSON(http.StatusOK, response.Response{
		Code: 200,
		Msg:  action + "成功，世界已重新启动",
		Data: map[string]interface{}{
			"buildId":       buildID,
			"beforeBuildId": beforeBuildID,
			"restarted":     restarted,
		},
	})
}

// startWorlds 按既有页面逻辑启动所有世界：先清理失效 UGC 记录，再启动并登记模组安装状态。
func (h *UpdateHandler) startWorlds(clusterName string) error {
	if h.modService != nil {
		if repairErr := h.modService.RepairStaleUgcRecords(clusterName); repairErr != nil {
			log.Println("清理失效 UGC 记录失败:", repairErr)
		}
	}
	if err := h.gameProcess.StartAll(clusterName); err != nil {
		return err
	}
	if h.modService != nil {
		if trackErr := h.modService.BeginInstallTracking(clusterName); trackErr != nil {
			log.Println("登记模组安装状态失败:", trackErr)
		}
	}
	return nil
}

// verifyUpdatedBuildID 核验更新后的 buildid。
//   - 普通更新要求 buildid 不下降；
//   - 若 buildid 没变化，则与已知最新 buildid 比较，避免 SteamCMD 退出码 0 但实际未更新；
//   - 深度修复只要求能读取到有效 buildid（修复不必然改变版本）。
func (h *UpdateHandler) verifyUpdatedBuildID(clusterName string, deep bool, before, after int64) error {
	if after <= 0 {
		return fmt.Errorf("buildid 无效: %d", after)
	}
	if deep {
		return nil
	}
	if before > 0 && after < before {
		return fmt.Errorf("更新后 buildid %d 低于更新前 %d", after, before)
	}
	if before > 0 && after == before {
		latest, err := h.resolver.GetLatestDstVersionInfo(clusterName)
		if err != nil {
			log.Printf("无法确认最新版本，跳过 buildid 变化校验: %v", err)
			return nil
		}
		if latest.Version > after {
			return fmt.Errorf("SteamCMD 返回成功，但本地 buildid 仍为 %d（最新为 %d），更新可能未生效", after, latest.Version)
		}
	}
	return nil
}

// ensureWorldsStopped 检查所有分片是否已停止；任何分片仍在运行都拒绝更新/修复。
func (h *UpdateHandler) ensureWorldsStopped(clusterName string) error {
	if h.gameProcess == nil || h.levelConfigUtils == nil {
		return nil
	}
	config, err := h.levelConfigUtils.GetLevelConfig(clusterName)
	if err != nil {
		return fmt.Errorf("读取世界配置失败: %w", err)
	}
	running := make([]string, 0, len(config.LevelList))
	for _, item := range config.LevelList {
		ok, statusErr := h.gameProcess.Status(clusterName, item.File)
		if statusErr != nil {
			return fmt.Errorf("检查世界状态失败: %w", statusErr)
		}
		if ok {
			running = append(running, item.File)
		}
	}
	if len(running) > 0 {
		return fmt.Errorf("世界仍在运行（%s），请先停止所有世界后再执行%s", strings.Join(running, ", "), "更新/修复")
	}
	return nil
}

// actionErrorMessage 把错误信息裁剪成适合页面展示的长度（保留结尾关键输出）。
func actionErrorMessage(action string, err error) string {
	const limit = 800
	text := strings.TrimSpace(err.Error())
	prefix := action + "失败: "
	if len(text) <= limit {
		return prefix + text
	}
	return prefix + "..." + text[len(text)-limit:]
}
