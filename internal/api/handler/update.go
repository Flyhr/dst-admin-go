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
}

// Update 生成 swagger 文档注释
// @Summary 更新游戏
// @Description 更新游戏
// @Tags update
// @Success 200 {object} response.Response
// @Router /api/game/update [get]
func (h *UpdateHandler) Update(ctx *gin.Context) {
	clusterName := context.GetClusterName(ctx)

	err := h.updateService.Update(clusterName)
	if err != nil {
		log.Println("更新游戏失败:", err)
		// 不再 panic：把 SteamCMD 的错误尾部返回给前端，便于页面提示具体原因。
		ctx.JSON(http.StatusOK, response.Response{
			Code: 500,
			Msg:  updateErrorMessage(err),
			Data: nil,
		})
		return
	}

	// 更新后核验：读取本地 buildid，确认安装记录可用（不能只看 SteamCMD 退出码）。
	buildID, verifyErr := h.resolver.GetLocalDstBuildId(clusterName)
	if verifyErr != nil {
		log.Println("更新后读取本地 buildid 失败:", verifyErr)
		ctx.JSON(http.StatusOK, response.Response{
			Code: 500,
			Msg:  "更新命令已执行，但无法读取本地 buildid 进行核验: " + verifyErr.Error(),
			Data: nil,
		})
		return
	}

	ctx.JSON(http.StatusOK, response.Response{
		Code: 200,
		Msg:  "update dst success",
		Data: map[string]interface{}{"buildId": buildID},
	})
}

// updateErrorMessage 把错误信息裁剪成适合页面展示的长度（保留结尾关键输出）。
func updateErrorMessage(err error) string {
	const limit = 800
	text := strings.TrimSpace(err.Error())
	if len(text) <= limit {
		return "更新游戏失败: " + text
	}
	return "更新游戏失败: ..." + text[len(text)-limit:]
}
