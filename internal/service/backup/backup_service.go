package backup

import (
	"dst-admin-go/internal/database"
	"dst-admin-go/internal/model"
	"dst-admin-go/internal/pkg/utils/dstUtils"
	"dst-admin-go/internal/pkg/utils/fileUtils"
	"dst-admin-go/internal/pkg/utils/shellUtils"
	"dst-admin-go/internal/pkg/utils/zip"
	"dst-admin-go/internal/service/archive"
	"dst-admin-go/internal/service/dstConfig"
	"dst-admin-go/internal/service/game"
	"errors"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

type BackupService struct {
	archive     *archive.PathResolver
	dstConfig   dstConfig.Config
	gameProcess game.Process
}

type BackupInfo struct {
	FileName   string    `json:"fileName"`
	FileSize   int64     `json:"fileSize"`
	CreateTime time.Time `json:"createTime"`
	Time       int64     `json:"time"`
}

type BackupSnapshot struct {
	Enable       int `json:"enable"`
	Interval     int `json:"interval"`
	MaxSnapshots int `json:"maxSnapshots"`
	IsCSave      int `json:"isCSave"`
}

func NewBackupService(archive *archive.PathResolver, dstConfig dstConfig.Config, gameProcess game.Process) *BackupService {
	return &BackupService{
		archive:     archive,
		dstConfig:   dstConfig,
		gameProcess: gameProcess,
	}
}

func (b *BackupService) GetBackupList(clusterName string) []BackupInfo {
	config, err := b.dstConfig.GetDstConfig(clusterName)
	if err != nil {
		log.Println("failed to get dst config:", err)
		return []BackupInfo{}
	}
	backupPath := config.Backup
	var backupList []BackupInfo

	if !fileUtils.Exists(backupPath) {
		return backupList
	}
	//获取文件或目录相关信息
	fileInfoList, err := ioutil.ReadDir(backupPath)
	if err != nil {
		log.Panicln(err)
	}

	for _, file := range fileInfoList {
		if file.IsDir() {
			continue
		}
		suffix := filepath.Ext(file.Name())
		if suffix == ".zip" || suffix == ".tar" {
			backup := BackupInfo{
				FileName:   file.Name(),
				FileSize:   file.Size(),
				CreateTime: file.ModTime(),
				Time:       file.ModTime().Unix(),
			}
			backupList = append(backupList, backup)
		}
	}

	return backupList

}

func (b *BackupService) RenameBackup(clusterName, fileName, newName string) error {
	config, err := b.dstConfig.GetDstConfig(clusterName)
	if err != nil {
		return fmt.Errorf("读取配置失败: %w", err)
	}
	backupPath := config.Backup
	sourcePath, err := safeBackupPath(backupPath, fileName, true)
	if err != nil {
		return err
	}
	oldExt := strings.ToLower(filepath.Ext(fileName))

	newName = strings.TrimSpace(newName)
	if newName == "" {
		return errors.New("新备份名称不能为空")
	}
	if filepath.Base(newName) != newName || strings.ContainsAny(newName, `/\`) {
		return errors.New("新备份名称不能包含路径分隔符")
	}
	newExt := strings.ToLower(filepath.Ext(newName))
	if newExt == "" {
		newName += oldExt
	} else if newExt != oldExt {
		return fmt.Errorf("新备份名称扩展名必须与原文件一致（%s）", oldExt)
	}
	targetPath, err := safeBackupPath(backupPath, newName, false)
	if err != nil {
		return err
	}
	if fileUtils.Exists(targetPath) {
		return fmt.Errorf("目标备份已存在: %s", newName)
	}
	if err := fileUtils.Rename(sourcePath, targetPath); err != nil {
		return fmt.Errorf("重命名失败: %w", err)
	}
	return nil
}

func (b *BackupService) DeleteBackup(clusterName string, fileNames []string) error {
	if len(fileNames) == 0 {
		return errors.New("未选择要删除的备份")
	}
	config, err := b.dstConfig.GetDstConfig(clusterName)
	if err != nil {
		return fmt.Errorf("读取配置失败: %w", err)
	}
	backupPath := config.Backup
	var errs []string
	for _, fileName := range fileNames {
		filePath, pathErr := safeBackupPath(backupPath, fileName, true)
		if pathErr != nil {
			errs = append(errs, pathErr.Error())
			continue
		}
		if delErr := fileUtils.DeleteFile(filePath); delErr != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", fileName, delErr))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("删除备份失败: %s", strings.Join(errs, "; "))
	}
	return nil
}

// BackupFilePath 返回备份文件的绝对路径，供下载使用；会校验文件名与路径安全。
func (b *BackupService) BackupFilePath(clusterName, fileName string) (string, error) {
	config, err := b.dstConfig.GetDstConfig(clusterName)
	if err != nil {
		return "", fmt.Errorf("读取配置失败: %w", err)
	}
	return safeBackupPath(config.Backup, fileName, true)
}

// RestoreBackup 恢复存档：
//  1. 停止所有世界；
//  2. 自动备份当前存档（恢复失败可回滚）；
//  3. 先解压到临时目录并校验 cluster.ini，再替换当前存档目录；
//  4. 重新生成 mods 安装清单；
//  5. 重新启动世界。
func (b *BackupService) RestoreBackup(clusterName, backupName string) error {
	config, err := b.dstConfig.GetDstConfig(clusterName)
	if err != nil {
		return fmt.Errorf("读取配置失败: %w", err)
	}
	backupPath := config.Backup
	sourcePath, err := safeBackupPath(backupPath, backupName, true)
	if err != nil {
		return err
	}
	if !strings.EqualFold(filepath.Ext(sourcePath), ".zip") {
		return errors.New("存档恢复只支持 .zip 备份文件")
	}

	// 1. 停止所有世界；失败时尝试恢复运行并返回错误。
	if b.gameProcess != nil {
		if stopErr := b.gameProcess.StopAll(clusterName); stopErr != nil {
			_ = b.gameProcess.StartAll(clusterName)
			return fmt.Errorf("停止世界失败，已取消恢复: %w", stopErr)
		}
	}
	restartWorlds := func() error {
		if b.gameProcess == nil {
			return nil
		}
		return b.gameProcess.StartAll(clusterName)
	}

	clusterPath := b.archive.ClusterPath(clusterName)
	// 2. 恢复前先备份当前存档（失败则回滚）。
	safetyName := "(restore-safety)" + b.GenGameBackUpName(clusterName)
	safetyPath := filepath.Join(backupPath, safetyName)
	if zipErr := zip.Zip(clusterPath, safetyPath); zipErr != nil {
		_ = restartWorlds()
		return fmt.Errorf("恢复前备份当前存档失败，已取消恢复: %w", zipErr)
	}

	// 3. 先解压到集群同级临时目录并校验，成功后再替换，避免备份损坏导致丢失当前存档。
	parentDir := filepath.Dir(clusterPath)
	tempDir, err := os.MkdirTemp(parentDir, ".restore-tmp-")
	if err != nil {
		_ = restartWorlds()
		return fmt.Errorf("创建临时目录失败: %w", err)
	}
	defer os.RemoveAll(tempDir)

	if unzipErr := zip.Unzip3(sourcePath, tempDir); unzipErr != nil {
		_ = restartWorlds()
		return fmt.Errorf("备份文件无效或解压失败，当前存档未改动: %w", unzipErr)
	}
	if !fileUtils.Exists(filepath.Join(tempDir, "cluster.ini")) {
		_ = restartWorlds()
		return errors.New("备份包中缺少 cluster.ini，当前存档未改动")
	}

	oldPath := clusterPath + ".restore-old-" + time.Now().Format("20060102150405")
	if renameErr := os.Rename(clusterPath, oldPath); renameErr != nil {
		_ = restartWorlds()
		return fmt.Errorf("替换当前存档失败: %w", renameErr)
	}
	if renameErr := os.Rename(tempDir, clusterPath); renameErr != nil {
		_ = os.Rename(oldPath, clusterPath)
		_ = restartWorlds()
		return fmt.Errorf("应用新存档失败，已回滚: %w", renameErr)
	}
	_ = os.RemoveAll(oldPath)

	// 4. 重新生成 mods 安装清单。
	if modoverride, readErr := fileUtils.ReadFile(b.archive.ModoverridesPath(clusterName, "Master")); readErr == nil {
		if modConfig, cfgErr := b.dstConfig.GetDstConfig(clusterName); cfgErr == nil {
			if setupErr := dstUtils.DedicatedServerModsSetup(modConfig, modoverride); setupErr != nil {
				log.Println("恢复后重建模组安装清单失败:", setupErr)
			}
		}
	}

	// 5. 重新启动世界；启动失败时如实返回（存档已恢复）。
	if startErr := restartWorlds(); startErr != nil {
		return fmt.Errorf("存档已恢复，但启动世界失败: %w", startErr)
	}
	log.Println("存档恢复完成，恢复前备份:", safetyName)
	return nil
}

func (b *BackupService) CreateBackup(clusterName, backupName string) error {
	config, err := b.dstConfig.GetDstConfig(clusterName)
	if err != nil {
		return fmt.Errorf("读取配置失败: %w", err)
	}
	backupPath := config.Backup
	if !fileUtils.Exists(backupPath) {
		return fmt.Errorf("备份目录不存在: %s", backupPath)
	}

	// 执行 CSave 命令
	if cmdErr := b.gameProcess.Command(clusterName, "Master", "c_save()"); cmdErr != nil {
		log.Println("CSave command error:", cmdErr)
	}
	// 等待保存完成
	time.Sleep(2 * time.Second)

	src := b.archive.ClusterPath(clusterName)
	if backupName == "" {
		backupName = b.GenGameBackUpName(clusterName)
	}
	target, err := safeBackupPath(backupPath, backupName, false)
	if err != nil {
		return err
	}
	if fileUtils.Exists(target) {
		return fmt.Errorf("备份文件已存在: %s", backupName)
	}
	if zipErr := zip.Zip(src, target); zipErr != nil {
		return fmt.Errorf("创建备份失败: %w", zipErr)
	}
	log.Println("创建备份成功:", backupName)
	return nil
}

// UploadBackup 保存上传的备份文件（校验文件名与扩展名）。
func (b *BackupService) UploadBackup(ctx *gin.Context, clusterName string) error {
	file, err := ctx.FormFile("file")
	if err != nil {
		return fmt.Errorf("读取上传文件失败: %w", err)
	}
	config, err := b.dstConfig.GetDstConfig(clusterName)
	if err != nil {
		return fmt.Errorf("读取配置失败: %w", err)
	}
	target, err := safeBackupPath(config.Backup, file.Filename, false)
	if err != nil {
		return err
	}
	if fileUtils.Exists(target) {
		return fmt.Errorf("备份文件已存在: %s", file.Filename)
	}
	if err := ctx.SaveUploadedFile(file, target); err != nil {
		return fmt.Errorf("保存上传文件失败: %w", err)
	}
	return nil
}

// safeBackupPath 校验备份文件名并返回备份目录内的完整路径。
// 只允许 basename、只允许 .zip/.tar，且必须位于备份目录内。
func safeBackupPath(backupDir, fileName string, mustExist bool) (string, error) {
	fileName = strings.TrimSpace(fileName)
	if fileName == "" {
		return "", errors.New("备份文件名不能为空")
	}
	if fileName == "." || fileName == ".." || filepath.Base(fileName) != fileName || strings.ContainsAny(fileName, `/\`) {
		return "", fmt.Errorf("非法备份文件名: %s", fileName)
	}
	ext := strings.ToLower(filepath.Ext(fileName))
	if ext != ".zip" && ext != ".tar" {
		return "", fmt.Errorf("不支持的备份文件类型: %s", fileName)
	}
	absDir, err := filepath.Abs(backupDir)
	if err != nil {
		return "", fmt.Errorf("备份目录无效: %w", err)
	}
	fullPath, err := filepath.Abs(filepath.Join(absDir, fileName))
	if err != nil {
		return "", fmt.Errorf("备份文件路径无效: %w", err)
	}
	rel, err := filepath.Rel(absDir, fullPath)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("备份文件路径越界: %s", fileName)
	}
	if mustExist && !fileUtils.Exists(fullPath) {
		return "", fmt.Errorf("备份文件不存在: %s", fileName)
	}
	return fullPath, nil
}

func (b *BackupService) ScheduleBackupSnapshots() {
	log.Println("开始创建快照")
	for {
		db := database.Db
		snapshot := model.BackupSnapshot{}
		db.First(&snapshot)
		if snapshot.Enable == 1 {
			if snapshot.Interval == 0 {
				snapshot.Interval = 8
			}
			if snapshot.MaxSnapshots == 0 {
				snapshot.MaxSnapshots = 6
			}
			time.Sleep(time.Duration(snapshot.Interval) * time.Minute)

			// 定时创建备份,每隔 x 分钟备份一次
			// Get the first available cluster config (or "MyCluster" as default)
			clusterName := "MyCluster"
			config, err := b.dstConfig.GetDstConfig(clusterName)
			if err != nil {
				log.Println("failed to get dst config for snapshot:", err)
				continue
			}
			if config.Cluster != "" {
				snapshotPrefix := "(snapshot)"
				if snapshot.IsCSave == 1 {
					// 执行 CSave 命令
					err := b.gameProcess.Command(config.Cluster, "Master", "c_save()")
					if err != nil {
						log.Println("CSave command error:", err)
					}
					// 等待保存完成
					time.Sleep(2 * time.Second)
				}
				b.CreateSnapshotBackup(snapshotPrefix, config.Cluster)
				// 删除快照
				b.DeleteBackupSnapshots(snapshotPrefix, snapshot.MaxSnapshots, config.Cluster, config.Backup)
			}
		} else {
			time.Sleep(1 * time.Minute)
		}
	}
}

func sumMd5(filePath string) string {
	// find save -type f -exec md5sum {} \; | awk '{print $1}' | sort | md5sum
	comamd := "find " + filePath + " -type f -exec md5sum {} \\; | awk '{print $1}' | sort | md5sum"
	info, err := shellUtils.ExecuteCommand(comamd)
	if err != nil {
		return ""
	}
	return info
}

func (b *BackupService) CreateSnapshotBackup(prefix, clusterName string) {

	config, err := b.dstConfig.GetDstConfig(clusterName)
	if err != nil {
		log.Println("failed to get dst config:", err)
		return
	}

	snapshotMd5FilePath := "./snapshotMd5"
	fileUtils.CreateFileIfNotExists(snapshotMd5FilePath)

	src := b.archive.ClusterPath(clusterName)
	dst := filepath.Join(config.Backup, b.GenBackUpSnapshotName(prefix, clusterName))
	log.Println("[Snapshot]正在定时创建游戏备份", "src: ", src, "dst: ", dst)
	err = zip.Zip(src, dst)
	if err != nil {
		log.Println("[Snapshot]create backup error", err)
	}
}

func (b *BackupService) DeleteBackupSnapshots(prefix string, maxSnapshots int, clusterName, backupPath string) {

	log.Println("[Snapshot]正在删除快照备份", "maxSnapshots", maxSnapshots, "clusterName: ", clusterName)

	backupList := b.GetBackupList(clusterName)
	var newBackupList []BackupInfo
	for i := range backupList {
		name := backupList[i].FileName
		if strings.HasPrefix(name, prefix) {
			newBackupList = append(newBackupList, backupList[i])
		}
	}
	if len(newBackupList) > maxSnapshots {
		deleteBackupList := newBackupList[:len(newBackupList)-maxSnapshots]
		for i := range deleteBackupList {
			filePath := filepath.Join(backupPath, deleteBackupList[i].FileName)
			log.Println("删除快照备份", filePath)
			if !fileUtils.Exists(filePath) {
				continue
			}
			err := fileUtils.DeleteFile(filePath)
			if err != nil {
				return
			}
		}
	}

}

func (b *BackupService) backupPath() string {
	// dstConfig := dstConfigUtils.GetDstConfig()
	// backupPath := dstConfig.Backup
	// if !fileUtils.Exists(backupPath) {
	// 	log.Panicln("backup path is not exists")
	// }
	// return backupPath
	return ""
}

var SeasonMap = map[string]string{
	"spring": "春天",
	"summer": "夏天",
	"autumn": "秋天",
	"winter": "冬天",
}

// GenGameBackUpName 备份名称增加存档信息如  猜猜我是谁的世界-10天-spring-1-20-2023071415
func (b *BackupService) GenGameBackUpName(clusterName string) string {
	// 简化实现，使用时间戳和集群名称
	backupName := time.Now().Format("2006年01月02日15点04分05秒") + "_" + clusterName + ".zip"

	return backupName
}

func (b *BackupService) GenBackUpSnapshotName(prefix, clusterName string) string {
	backupName := b.GenGameBackUpName(clusterName)
	backupName = prefix + backupName
	return backupName
}
