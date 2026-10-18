package handler

import (
	"dst-admin-go/internal/database"
	"dst-admin-go/internal/model"
	"dst-admin-go/internal/pkg/context"
	"dst-admin-go/internal/pkg/response"
	"dst-admin-go/internal/service/backup"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"
)

type BackupHandler struct {
	backupService *backup.BackupService
}

func NewBackupHandler(backupService *backup.BackupService) *BackupHandler {
	return &BackupHandler{
		backupService: backupService,
	}
}

func (h *BackupHandler) RegisterRoute(router *gin.RouterGroup) {
	router.GET("/api/game/backup", h.GetBackupList)
	router.POST("/api/game/backup", h.CreateBackup)
	router.DELETE("/api/game/backup", h.DeleteBackup)
	router.PUT("/api/game/backup", h.RenameBackup)
	router.GET("/api/game/backup/download", h.DownloadBackup)
	router.POST("/api/game/backup/upload", h.UploadBackup)
	router.GET("/api/game/backup/restore", h.RestoreBackup)
	// 兼容旧路径（历史版本前端使用过 /backup/restore）。
	router.GET("/backup/restore", h.RestoreBackup)
	router.POST("/api/game/backup/snapshot/setting", h.SaveBackupSnapshotsSetting)
	router.GET("/api/game/backup/snapshot/setting", h.GetBackupSnapshotsSetting)
	router.GET("/api/game/backup/snapshot/list", h.BackupSnapshotsList)
}

func fail(ctx *gin.Context, err error) {
	ctx.JSON(http.StatusOK, response.Response{Code: 500, Msg: err.Error(), Data: nil})
}

// DeleteBackup 删除备份
// @Summary 删除备份
// @Description 删除指定的备份文件
// @Tags backup
// @Router /api/game/backup [delete]
func (h *BackupHandler) DeleteBackup(ctx *gin.Context) {
	var body struct {
		FileNames []string `json:"fileNames"`
	}
	if err := ctx.ShouldBindJSON(&body); err != nil {
		fail(ctx, fmt.Errorf("参数错误: %w", err))
		return
	}
	clusterName := context.GetClusterName(ctx)
	if err := h.backupService.DeleteBackup(clusterName, body.FileNames); err != nil {
		log.Println("删除备份失败:", err)
		fail(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, response.Response{Code: 200, Msg: "delete backups success", Data: nil})
}

// DownloadBackup 下载备份
// @Summary 下载备份
// @Description 下载指定的备份文件
// @Tags backup
// @Router /api/game/backup/download [get]
func (h *BackupHandler) DownloadBackup(ctx *gin.Context) {
	clusterName := context.GetClusterName(ctx)
	fileName := ctx.Query("fileName")
	filePath, err := h.backupService.BackupFilePath(clusterName, fileName)
	if err != nil {
		// 下载接口不能返回 JSON（前端按二进制接收），这里用真实 HTTP 状态码表示失败。
		ctx.JSON(http.StatusBadRequest, response.Response{Code: 500, Msg: err.Error(), Data: nil})
		return
	}
	ctx.Header("Content-Type", "application/octet-stream")
	ctx.Header("Content-Disposition", downloadContentDisposition(fileName))
	ctx.File(filePath)
}

// downloadContentDisposition 生成支持中文文件名的下载响应头。
func downloadContentDisposition(fileName string) string {
	fallback := fileName
	for _, r := range fileName {
		if r > 127 {
			fallback = "backup" + filepath.Ext(fileName)
			break
		}
	}
	return fmt.Sprintf("attachment; filename=%q; filename*=UTF-8''%s", fallback, url.PathEscape(fileName))
}

// GetBackupList 获取备份列表
// @Summary 获取备份列表
// @Description 获取当前集群的所有备份文件列表
// @Tags backup
// @Router /api/game/backup [get]
func (h *BackupHandler) GetBackupList(ctx *gin.Context) {
	clusterName := context.GetClusterName(ctx)
	list, err := h.backupService.GetBackupList(clusterName)
	if err != nil {
		log.Println("获取备份列表失败:", err)
		fail(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, response.Response{
		Code: 200,
		Msg:  "get backup list success",
		Data: list,
	})
}

// RenameBackup 重命名备份
// @Summary 重命名备份
// @Description 重命名指定的备份文件
// @Tags backup
// @Router /api/game/backup [put]
func (h *BackupHandler) RenameBackup(ctx *gin.Context) {
	var body struct {
		FileName string `json:"fileName"`
		NewName  string `json:"newName"`
	}
	if err := ctx.ShouldBindJSON(&body); err != nil {
		fail(ctx, fmt.Errorf("参数错误: %w", err))
		return
	}
	clusterName := context.GetClusterName(ctx)
	if err := h.backupService.RenameBackup(clusterName, body.FileName, body.NewName); err != nil {
		log.Println("重命名备份失败:", err)
		fail(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, response.Response{Code: 200, Msg: "rename backup success", Data: nil})
}

// RestoreBackup 恢复备份（会停止世界、恢复存档并重新启动世界）
// @Summary 恢复备份
// @Description 从备份文件恢复游戏存档
// @Tags backup
// @Router /api/game/backup/restore [get]
func (h *BackupHandler) RestoreBackup(ctx *gin.Context) {
	clusterName := context.GetClusterName(ctx)
	backupName := ctx.Query("backupName")
	if err := h.backupService.RestoreBackup(clusterName, backupName); err != nil {
		log.Println("恢复备份失败:", err)
		fail(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, response.Response{Code: 200, Msg: "restore backup success", Data: nil})
}

// UploadBackup 上传备份
// @Summary 上传备份
// @Description 上传新的备份文件
// @Tags backup
// @Router /api/game/backup/upload [post]
func (h *BackupHandler) UploadBackup(ctx *gin.Context) {
	clusterName := context.GetClusterName(ctx)
	if err := h.backupService.UploadBackup(ctx, clusterName); err != nil {
		log.Println("上传备份失败:", err)
		fail(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, response.Response{Code: 200, Msg: "upload backup success", Data: nil})
}

// CreateBackup 创建备份
// @Summary 创建备份
// @Description 创建新的游戏存档备份
// @Tags backup
// @Router /api/game/backup [post]
func (h *BackupHandler) CreateBackup(ctx *gin.Context) {
	var body struct {
		BackupName string `json:"backupName"`
	}
	if err := ctx.ShouldBind(&body); err != nil {
		body.BackupName = ""
	}
	clusterName := context.GetClusterName(ctx)
	if err := h.backupService.CreateBackup(clusterName, body.BackupName); err != nil {
		log.Println("创建备份失败:", err)
		fail(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, response.Response{Code: 200, Msg: "create backup success", Data: nil})
}

// SaveBackupSnapshotsSetting 保存快照设置
// @Summary 保存快照设置
// @Description 保存自动快照备份的设置
// @Tags backup
// @Router /api/game/backup/snapshot/setting [post]
func (h *BackupHandler) SaveBackupSnapshotsSetting(ctx *gin.Context) {
	var backupSnapshot model.BackupSnapshot
	var oldBackupSnapshot model.BackupSnapshot
	if err := ctx.ShouldBind(&backupSnapshot); err != nil {
		fail(ctx, fmt.Errorf("参数错误: %w", err))
		return
	}
	db := database.Db
	db.First(&oldBackupSnapshot)

	oldBackupSnapshot.Enable = backupSnapshot.Enable
	oldBackupSnapshot.Interval = backupSnapshot.Interval
	oldBackupSnapshot.MaxSnapshots = backupSnapshot.MaxSnapshots
	oldBackupSnapshot.IsCSave = backupSnapshot.IsCSave

	db.Save(&oldBackupSnapshot)

	ctx.JSON(http.StatusOK, response.Response{Code: 200, Msg: "success", Data: oldBackupSnapshot})
}

// GetBackupSnapshotsSetting 获取快照设置
// @Summary 获取快照设置
// @Description 获取自动快照备份的设置
// @Tags backup
// @Router /api/game/backup/snapshot/setting [get]
func (h *BackupHandler) GetBackupSnapshotsSetting(ctx *gin.Context) {
	var oldBackupSnapshot model.BackupSnapshot
	db := database.Db
	db.First(&oldBackupSnapshot)

	ctx.JSON(http.StatusOK, response.Response{Code: 200, Msg: "success", Data: oldBackupSnapshot})
}

// BackupSnapshotsList 获取快照列表
// @Summary 获取快照列表
// @Description 获取当前集群的所有快照备份列表
// @Tags backup
// @Router /api/game/backup/snapshot/list [get]
func (h *BackupHandler) BackupSnapshotsList(ctx *gin.Context) {
	clusterName := context.GetClusterName(ctx)

	var snapshotBackupList []backup.BackupInfo
	backupList, err := h.backupService.GetBackupList(clusterName)
	if err != nil {
		fail(ctx, err)
		return
	}
	for i := range backupList {
		name := backupList[i].FileName
		if strings.HasPrefix(name, "(snapshot)") && strings.Contains(name, clusterName) {
			snapshotBackupList = append(snapshotBackupList, backupList[i])
		}
	}

	ctx.JSON(http.StatusOK, response.Response{Code: 200, Msg: "success", Data: snapshotBackupList})
}
