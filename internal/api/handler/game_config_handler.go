package handler

import (
	"dst-admin-go/internal/pkg/context"
	"dst-admin-go/internal/pkg/response"
	"dst-admin-go/internal/service/gameConfig"
	"errors"
	"log"
	"net/http"
	"os"

	"github.com/gin-gonic/gin"
)

type GameConfigHandler struct {
	gameConfig *gameConfig.GameConfig
}

func NewGameConfigHandler(gameConfig *gameConfig.GameConfig) *GameConfigHandler {
	return &GameConfigHandler{
		gameConfig: gameConfig,
	}
}

func (p *GameConfigHandler) RegisterRoute(router *gin.RouterGroup) {
	router.GET("/api/game/8level/clusterIni", p.GetClusterIni)
	router.POST("/api/game/8level/clusterIni", p.SaveClusterIni)
	router.GET("/api/game/8level/adminilist", p.GetAdminList)
	router.POST("/api/game/8level/adminilist", p.SaveAdminList)
	router.GET("/api/game/8level/blacklist", p.GetBlackList)
	router.POST("/api/game/8level/blacklist", p.SaveBlackList)
	router.GET("/api/game/8level/whitelist", p.GetWhithList)
	router.POST("/api/game/8level/whitelist", p.SaveWhithList)

	router.GET("/api/game/config", p.GetConfig)
	router.GET("/api/game/config/inspect", p.InspectClusterConfig)
	router.GET("/api/game/config/cluster-name", p.GetClusterName)
	router.PUT("/api/game/config/cluster-name", p.SaveClusterName)
	router.POST("/api/game/config", p.SaveConfig)
}

func (p *GameConfigHandler) GetClusterName(ctx *gin.Context) {
	name, err := p.gameConfig.GetClusterName(context.GetClusterName(ctx))
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, os.ErrNotExist) {
			status = http.StatusNotFound
		}
		ctx.JSON(status, response.Response{Code: status, Msg: err.Error()})
		return
	}
	ctx.JSON(http.StatusOK, response.Response{Code: http.StatusOK, Msg: "success", Data: gin.H{"cluster_name": name}})
}

func (p *GameConfigHandler) SaveClusterName(ctx *gin.Context) {
	var payload struct {
		ClusterName string `json:"cluster_name"`
	}
	if err := ctx.ShouldBindJSON(&payload); err != nil {
		ctx.JSON(http.StatusBadRequest, response.Response{Code: http.StatusBadRequest, Msg: "请求体无效，需要 cluster_name 字符串"})
		return
	}
	if err := p.gameConfig.SaveClusterName(context.GetClusterName(ctx), payload.ClusterName); err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, gameConfig.ErrInvalidClusterName) {
			status = http.StatusBadRequest
		} else if errors.Is(err, os.ErrNotExist) {
			status = http.StatusNotFound
		}
		ctx.JSON(status, response.Response{Code: status, Msg: err.Error()})
		return
	}
	ctx.JSON(http.StatusOK, response.Response{Code: http.StatusOK, Msg: "success"})
}

func (p *GameConfigHandler) InspectClusterConfig(ctx *gin.Context) {
	view, err := p.gameConfig.InspectClusterConfig(context.GetClusterName(ctx))
	if err != nil {
		ctx.JSON(http.StatusBadRequest, response.Response{
			Code: http.StatusBadRequest,
			Msg:  "无法读取集群配置: " + err.Error(),
		})
		return
	}
	ctx.JSON(http.StatusOK, response.Response{Code: http.StatusOK, Msg: "success", Data: view})
}

// GetClusterIni 获取房间 cluster.ini 配置 swagger 注释
// @Summary 获取房间 cluster.ini 配置
// @Description 获取房间 cluster.ini 配置
// @Tags gameConfig
// @Accept json
// @Produce json
// @Success 200 {object} response.Response{data=gameConfig.ClusterIniConfig}
// @Router /api/game/config/clusterIni [get]
func (p *GameConfigHandler) GetClusterIni(ctx *gin.Context) {
	clusterName := context.GetClusterName(ctx)
	config, err := p.gameConfig.GetClusterIniConfig(clusterName)
	if err != nil {
		ctx.JSON(500, response.Response{
			Code: http.StatusInternalServerError,
			Msg:  err.Error(),
			Data: nil,
		})
		return
	}
	ctx.JSON(200, response.Response{
		Code: http.StatusOK,
		Msg:  "success",
		Data: config,
	})
}

// SaveClusterIni 保存房间 cluster.ini 配置 swagger 注释
// @Summary 保存房间 cluster.ini 配置
// @Description 保存房间 cluster.ini 配置
// @Tags gameConfig
// @Accept json
// @Produce json
// @Param config body gameConfig.ClusterIniConfig true "cluster.ini 配置"
// @Success 200 {object} response.Response
// @Router /api/game/config/clusterIni [post]
func (p *GameConfigHandler) SaveClusterIni(ctx *gin.Context) {
	clusterName := context.GetClusterName(ctx)
	var config gameConfig.ClusterIniConfig
	if err := ctx.ShouldBindJSON(&config); err != nil {
		ctx.JSON(400, response.Response{
			Code: http.StatusBadRequest,
			Msg:  "Invalid request body",
			Data: nil,
		})
		return
	}
	err := p.gameConfig.SaveClusterIniConfig(clusterName, &config)
	if err != nil {
		ctx.JSON(500, response.Response{
			Code: http.StatusInternalServerError,
			Msg:  err.Error(),
			Data: nil,
		})
		return
	}
	ctx.JSON(200, response.Response{
		Code: http.StatusOK,
		Msg:  "success",
		Data: nil,
	})
}

// GetAdminList 获取房间 adminlist.txt 配置 swagger 注释
// @Summary 获取房间 adminlist.txt 配置
// @Description 获取房间 adminlist.txt 配置
// @Tags gameConfig
// @Accept json
// @Produce json
// @Success 200 {object} response.Response{data=[]string}
// @Router /api/game/config/adminlist [get]
func (p *GameConfigHandler) GetAdminList(ctx *gin.Context) {
	clusterName := context.GetClusterName(ctx)
	list, err := p.gameConfig.GetAdminList(clusterName)
	if err != nil {
		ctx.JSON(500, response.Response{
			Code: http.StatusInternalServerError,
			Msg:  err.Error(),
			Data: nil,
		})
		return
	}
	ctx.JSON(200, response.Response{
		Code: http.StatusOK,
		Msg:  "success",
		Data: list,
	})
}

// SaveAdminList 保存房间 adminlist.txt 配置 swagger 注释
// @Summary 保存房间 adminlist.txt 配置
// @Description 保存房间 adminlist.txt 配置
// @Tags gameConfig
// @Accept json
// @Produce json
// @Param list body []string true "adminlist.txt 配置"
// @Success 200 {object} response.Response
// @Router /api/game/config/adminlist [post]
func (p *GameConfigHandler) SaveAdminList(ctx *gin.Context) {
	clusterName := context.GetClusterName(ctx)
	var payload struct {
		List []string `json:"adminlist"`
	}
	if err := ctx.ShouldBindJSON(&payload); err != nil {
		ctx.JSON(400, response.Response{
			Code: http.StatusBadRequest,
			Msg:  "Invalid request body",
			Data: nil,
		})
		return
	}
	err := p.gameConfig.SaveAdminList(clusterName, payload.List)
	if err != nil {
		ctx.JSON(500, response.Response{
			Code: http.StatusInternalServerError,
			Msg:  err.Error(),
			Data: nil,
		})
		return
	}
	ctx.JSON(200, response.Response{
		Code: http.StatusOK,
		Msg:  "success",
		Data: nil,
	})
}

// GetBlackList 获取房间 blacklist.txt 配置 swagger 注释
// @Summary 获取房间 blacklist.txt 配置
// @Description 获取房间 blacklist.txt 配置
// @Tags gameConfig
// @Accept json
// @Produce json
// @Success 200 {object} response.Response{data=[]string}
// @Router /api/game/config/blacklist [get]
func (p *GameConfigHandler) GetBlackList(ctx *gin.Context) {
	clusterName := context.GetClusterName(ctx)
	list, err := p.gameConfig.GetBlackList(clusterName)
	if err != nil {
		ctx.JSON(500, response.Response{
			Code: http.StatusInternalServerError,
			Msg:  err.Error(),
			Data: nil,
		})
		return
	}
	ctx.JSON(200, response.Response{
		Code: http.StatusOK,
		Msg:  "success",
		Data: list,
	})
}

// SaveBlackList 保存房间 blacklist.txt 配置 swagger 注释
// @Summary 保存房间 blacklist.txt 配置
// @Description 保存房间 blacklist.txt 配置
// @Tags gameConfig
// @Accept json
// @Produce json
// @Param list body []string true "blacklist.txt 配置"
// @Success 200 {object} response.Response
// @Router /api/game/config/blacklist [post]
func (p *GameConfigHandler) SaveBlackList(ctx *gin.Context) {
	clusterName := context.GetClusterName(ctx)
	var payload struct {
		List []string `json:"blacklist"`
	}
	if err := ctx.ShouldBindJSON(&payload); err != nil {
		ctx.JSON(400, response.Response{
			Code: http.StatusBadRequest,
			Msg:  "Invalid request body",
			Data: nil,
		})
		return
	}
	err := p.gameConfig.SaveBlackList(clusterName, payload.List)
	if err != nil {
		ctx.JSON(500, response.Response{
			Code: http.StatusInternalServerError,
			Msg:  err.Error(),
			Data: nil,
		})
		return
	}
	ctx.JSON(200, response.Response{
		Code: http.StatusOK,
		Msg:  "success",
		Data: nil,
	})
}

// GetWhithList 获取房间 whitelist.txt 配置 swagger 注释
// @Summary 获取房间 whitelist.txt 配置
// @Description 获取房间 whitelist.txt 配置
// @Tags gameConfig
// @Accept json
// @Produce json
// @Success 200 {object} response.Response{data=[]string}
// @Router /api/game/config/whitelist [get]
func (p *GameConfigHandler) GetWhithList(ctx *gin.Context) {
	clusterName := context.GetClusterName(ctx)
	list, err := p.gameConfig.GetWhithList(clusterName)
	if err != nil {
		ctx.JSON(500, response.Response{
			Code: http.StatusInternalServerError,
			Msg:  err.Error(),
			Data: nil,
		})
		return
	}
	ctx.JSON(200, response.Response{
		Code: http.StatusOK,
		Msg:  "success",
		Data: list,
	})
}

// SaveWhithList 保存房间 whitelist.txt 配置 swagger 注释
// @Summary 保存房间 whitelist.txt 配置
// @Description 保存房间 whitelist.txt 配置
// @Tags gameConfig
// @Accept json
// @Produce json
// @Param list body []string true "whitelist.txt 配置"
// @Success 200 {object} response.Response
// @Router /api/game/config/whitelist [post]
func (p *GameConfigHandler) SaveWhithList(ctx *gin.Context) {
	clusterName := context.GetClusterName(ctx)
	var payload struct {
		List []string `json:"whitelist"`
	}
	if err := ctx.ShouldBindJSON(&payload); err != nil {
		ctx.JSON(400, response.Response{
			Code: http.StatusBadRequest,
			Msg:  "Invalid request body",
			Data: nil,
		})
		return
	}
	err := p.gameConfig.SaveWhithList(clusterName, payload.List)
	if err != nil {
		ctx.JSON(500, response.Response{
			Code: http.StatusInternalServerError,
			Msg:  err.Error(),
			Data: nil,
		})
		return
	}
	ctx.JSON(200, response.Response{
		Code: http.StatusOK,
		Msg:  "success",
		Data: nil,
	})

}

// GetConfig 获取房间配置 swagger 注释
// @Summary 获取房间配置
// @Description 获取房间配置
// @Tags gameConfig
// @Accept json
// @Produce json
// @Success 200 {object} response.Response{data=gameConfig.HomeConfigVO}
// @Router /api/game/config [get]
func (p *GameConfigHandler) GetConfig(ctx *gin.Context) {
	clusterName := context.GetClusterName(ctx)
	config, err := p.gameConfig.GetHomeConfig(clusterName)
	if err != nil {
		ctx.JSON(200, response.Response{
			Code: 500,
			Msg:  err.Error(),
			Data: nil,
		})
		return
	}
	ctx.JSON(200, response.Response{
		Code: 200,
		Msg:  "success",
		Data: config,
	})
}

func (p *GameConfigHandler) SaveConfig(ctx *gin.Context) {
	clusterName := context.GetClusterName(ctx)
	config := gameConfig.HomeConfigVO{}
	err := ctx.ShouldBind(&config)
	if err != nil {
		ctx.JSON(http.StatusBadRequest, response.Response{
			Code: 400,
			Msg:  "Invalid request body",
		})
		return
	}
	log.Println(config)
	p.gameConfig.SaveConfig(clusterName, config)

	ctx.JSON(http.StatusOK, response.Response{
		Code: 200,
		Msg:  "save dst server config success",
	})
}
