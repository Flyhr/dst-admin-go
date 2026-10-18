package handler

import (
	"dst-admin-go/internal/pkg/context"
	"dst-admin-go/internal/pkg/response"
	"dst-admin-go/internal/service/archive"
	"dst-admin-go/internal/service/game"
	"dst-admin-go/internal/service/levelConfig"
	"dst-admin-go/internal/service/update"
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

type UpdateHandler struct {
	updateService    update.Update
	resolver         *archive.PathResolver
	gameProcess      game.Process
	levelConfigUtils *levelConfig.LevelConfigUtils
}

func NewUpdateHandler(
	update update.Update,
	resolver *archive.PathResolver,
	gameProcess game.Process,
	levelConfigUtils *levelConfig.LevelConfigUtils,
) *UpdateHandler {
	return &UpdateHandler{
		updateService:    update,
		resolver:         resolver,
		gameProcess:      gameProcess,
		levelConfigUtils: levelConfigUtils,
	}
}

func (h *UpdateHandler) RegisterRoute(router *gin.RouterGroup) {
	router.GET("/api/game/update", h.Update)
	router.GET("/api/game/repair", h.Repair)
	router.GET("/api/game/version", h.Version)
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

func (h *UpdateHandler) run(ctx *gin.Context, deep bool) {
	clusterName := context.GetClusterName(ctx)
	action := "更新游戏"
	if deep {
		action = "深度修复游戏"
	}

	// 后端强制校验世界已停止：无论从页面、定时任务还是直接调接口，都不允许在游戏运行时改文件。
	if err := h.ensureWorldsStopped(clusterName); err != nil {
		log.Println(action+"被拒绝:", err)
		ctx.JSON(http.StatusOK, response.Response{Code: 500, Msg: err.Error()})
		return
	}

	beforeBuildID, _ := h.resolver.GetLocalDstBuildId(clusterName)

	var err error
	if deep {
		err = h.updateService.Repair(clusterName)
	} else {
		err = h.updateService.Update(clusterName)
	}
	if err != nil {
		log.Println(action+"失败:", err)
		ctx.JSON(http.StatusOK, response.Response{
			Code: 500,
			Msg:  actionErrorMessage(action, err),
			Data: nil,
		})
		return
	}

	// 更新/修复后核验：读取本地 buildid，确认安装记录可用（不能只看 SteamCMD 退出码）。
	buildID, verifyErr := h.resolver.GetLocalDstBuildId(clusterName)
	if verifyErr != nil {
		log.Println(action + "后读取本地 buildid 失败: " + verifyErr.Error())
		ctx.JSON(http.StatusOK, response.Response{
			Code: 500,
			Msg:  action + "命令已执行，但无法读取本地 buildid 进行核验: " + verifyErr.Error(),
			Data: nil,
		})
		return
	}
	// 更新完成后清空版本缓存，页面下一次检查会重新获取真实数据。
	h.resolver.InvalidateVersionCache(clusterName)

	msg := "update dst success"
	if deep {
		msg = "repair dst success"
	}
	ctx.JSON(http.StatusOK, response.Response{
		Code: 200,
		Msg:  msg,
		Data: map[string]interface{}{
			"buildId":       buildID,
			"beforeBuildId": beforeBuildID,
		},
	})
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
