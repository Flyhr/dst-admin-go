package handler

import (
	"dst-admin-go/internal/pkg/context"
	"dst-admin-go/internal/pkg/response"
	"dst-admin-go/internal/service/archive"
	"dst-admin-go/internal/service/update"
	"log"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

type UpdateHandler struct {
	updateService update.Update
	resolver      *archive.PathResolver
}

func NewUpdateHandler(update update.Update, resolver *archive.PathResolver) *UpdateHandler {
	return &UpdateHandler{
		updateService: update,
		resolver:      resolver,
	}
}

func (h *UpdateHandler) RegisterRoute(router *gin.RouterGroup) {
	router.GET("/api/game/update", h.Update)
	router.GET("/api/game/repair", h.Repair)
}

// Update 生成 swagger 文档注释
// @Summary 更新游戏
// @Description 更新游戏
// @Tags update
// @Success 200 {object} response.Response
// @Router /api/game/update [get]
func (h *UpdateHandler) Update(ctx *gin.Context) {
	h.run(ctx, false)
}

// Repair 深度修复游戏（SteamCMD validate 全量校验）。
// @Summary 深度修复游戏
// @Description 停止世界后使用 SteamCMD validate 全量校验/修复游戏文件，然后核验 buildid
// @Tags update
// @Router /api/game/repair [get]
func (h *UpdateHandler) Repair(ctx *gin.Context) {
	h.run(ctx, true)
}

func (h *UpdateHandler) run(ctx *gin.Context, deep bool) {
	clusterName := context.GetClusterName(ctx)
	action := "更新游戏"
	if deep {
		action = "深度修复游戏"
	}

	var err error
	if deep {
		err = h.updateService.Repair(clusterName)
	} else {
		err = h.updateService.Update(clusterName)
	}
	if err != nil {
		log.Println(action+"失败:", err)
		// 不再 panic：把 SteamCMD 的错误尾部返回给前端，便于页面提示具体原因。
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

	msg := "update dst success"
	if deep {
		msg = "repair dst success"
	}
	ctx.JSON(http.StatusOK, response.Response{
		Code: 200,
		Msg:  msg,
		Data: map[string]interface{}{"buildId": buildID},
	})
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
