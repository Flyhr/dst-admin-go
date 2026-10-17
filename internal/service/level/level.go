package level

import (
	"dst-admin-go/internal/pkg/utils/dstUtils"
	"dst-admin-go/internal/pkg/utils/fileUtils"
	"dst-admin-go/internal/service/archive"
	"dst-admin-go/internal/service/dstConfig"
	"dst-admin-go/internal/service/game"
	"dst-admin-go/internal/service/gameConfig"
	"dst-admin-go/internal/service/levelConfig"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/go-ini/ini"
)

// LevelService 关卡服务结构体
type LevelService struct {
	gameProcess      game.Process
	dstConfig        dstConfig.Config
	resolver         *archive.PathResolver
	levelConfigUtils *levelConfig.LevelConfigUtils
}

// NewLevelService 创建关卡服务实例
func NewLevelService(gameProcess game.Process, dstConfig dstConfig.Config, resolver *archive.PathResolver, levelConfigUtils *levelConfig.LevelConfigUtils) *LevelService {
	return &LevelService{
		gameProcess:      gameProcess,
		dstConfig:        dstConfig,
		resolver:         resolver,
		levelConfigUtils: levelConfigUtils,
	}
}

// GetLevelList 获取关卡列表
func (l *LevelService) GetLevelList(clusterName string) []levelConfig.LevelInfo {
	config, err := l.levelConfigUtils.GetLevelConfig(clusterName)
	if err != nil {
		return []levelConfig.LevelInfo{}
	}
	var levels []levelConfig.LevelInfo
	if len(config.LevelList) == 0 {
		masterLevelPath := filepath.Join(l.resolver.ClusterPath(clusterName), "Master")
		if !fileUtils.Exists(masterLevelPath) {
			master := levelConfig.LevelInfo{
				IsMaster:          true,
				LevelName:         "Forest",
				Uuid:              "Master",
				Leveldataoverride: "return {}",
				Modoverrides:      "return {}",
				ServerIni:         levelConfig.NewMasterServerIni(),
			}
			l.initLevel(filepath.Join(l.resolver.ClusterPath(clusterName), "Master"), &master, true)
			levels = append([]levelConfig.LevelInfo{}, master)
			config.LevelList = append(config.LevelList, levelConfig.Item{
				Name: "Forest",
				File: "Master",
			})
			err = l.levelConfigUtils.SaveLevelConfig(clusterName, config)
			if err != nil {
				log.Println(err)
			}
			return l.attachModConfigScope(clusterName, levels)
		} else {
			// 读取现有的 Master 世界配置
			master := l.GetLevel(clusterName, "Master")
			levels = append([]levelConfig.LevelInfo{}, master)
			config.LevelList = append(config.LevelList, levelConfig.Item{
				Name: "Forest",
				File: "Master",
			})
			cavesLevelPath := filepath.Join(l.resolver.ClusterPath(clusterName), "Caves")
			if fileUtils.Exists(cavesLevelPath) {
				config.LevelList = append(config.LevelList, levelConfig.Item{
					Name: "Caves",
					File: "Caves",
				})
				caves := l.GetLevel(clusterName, "Caves")
				levels = append(levels, caves)
			}
			err = l.levelConfigUtils.SaveLevelConfig(clusterName, config)
			if err != nil {
				log.Println(err)
			}
			return l.attachModConfigScope(clusterName, levels)
		}
	}
	for i := range config.LevelList {
		level1 := levelConfig.LevelInfo{}
		level1.LevelName = config.LevelList[i].Name
		level1.Uuid = config.LevelList[i].File
		level1.RunVersion = config.LevelList[i].RunVersion
		world := l.GetLevel(clusterName, config.LevelList[i].File)
		level1.Leveldataoverride = world.Leveldataoverride
		level1.Modoverrides = world.Modoverrides
		level1.ServerIni = world.ServerIni
		levels = append(levels, level1)
	}
	return l.attachModConfigScope(clusterName, levels)
}

// GetLevel 获取单个关卡配置
func (l *LevelService) GetLevel(clusterName string, levelName string) levelConfig.LevelInfo {
	levelFolderPath := filepath.Join(l.resolver.ClusterPath(clusterName), levelName)
	config, _ := l.levelConfigUtils.GetLevelConfig(clusterName)
	name := ""
	for _, item := range config.LevelList {
		if item.File == levelName {
			name = item.Name
		}
	}

	// 沿用现有世界实际使用的覆盖文件。
	lPath := l.resolver.LeveldataoverridePath(clusterName, levelName)
	leveldataoverride, err := fileUtils.ReadFile(lPath)
	if err != nil {
		leveldataoverride = "return {}"
	}

	// 读取 modoverrides.lua。缺失文件沿用空配置；断链或无权限不能伪装成空配置。
	mPath := filepath.Join(levelFolderPath, "modoverrides.lua")
	modoverrides, err := fileUtils.ReadFile(mPath)
	modConfigError := ""
	if err != nil {
		if _, _, resolveErr := resolveModTarget(filepath.Dir(levelFolderPath), levelName); resolveErr != nil {
			modConfigError = resolveErr.Error()
		} else {
			modoverrides = "return {}"
		}
	}

	// 读取 server.ini
	sPath := filepath.Join(levelFolderPath, "server.ini")
	serverIni := l.GetServerIni(sPath, levelName == "Master")

	return levelConfig.LevelInfo{
		IsMaster:          levelName == "Master",
		LevelName:         name,
		Uuid:              levelName,
		Leveldataoverride: leveldataoverride,
		Modoverrides:      modoverrides,
		ModConfigError:    modConfigError,
		ServerIni:         serverIni,
	}
}

func (l *LevelService) GetServerIni(filepath string, isMaster bool) levelConfig.ServerIni {
	fileUtils.CreateFileIfNotExists(filepath)
	var serverPortDefault uint = 10998
	idDefault := 10010

	if isMaster {
		serverPortDefault = 10999
		idDefault = 10000
	}

	serverIni := levelConfig.NewCavesServerIni()
	// 加载 INI 文件
	cfg, err := ini.Load(filepath)
	if err != nil {
		return serverIni
	}

	// [NETWORK]
	NETWORK := cfg.Section("NETWORK")

	serverIni.ServerPort = NETWORK.Key("server_port").MustUint(serverPortDefault)

	// [SHARD]
	SHARD := cfg.Section("SHARD")

	serverIni.IsMaster = SHARD.Key("is_master").MustBool(isMaster)
	serverIni.Name = SHARD.Key("name").String()
	serverIni.Id = SHARD.Key("id").MustUint(uint(idDefault))

	// [ACCOUNT]
	ACCOUNT := cfg.Section("ACCOUNT")
	serverIni.EncodeUserPath = ACCOUNT.Key("encode_user_path").MustBool(true)

	// [STEAM]
	STEAM := cfg.Section("STEAM")

	serverIni.AuthenticationPort = STEAM.Key("authentication_port").MustUint(8766)
	serverIni.MasterServerPort = STEAM.Key("master_server_port").MustUint(27016)

	return serverIni
}

// UpdateLevels 更新多个关卡配置
func (l *LevelService) UpdateLevels(clusterName string, levels []levelConfig.LevelInfo) error {
	return l.saveLevels(clusterName, levels)
}

// UpdateLevel 更新单个关卡配置
func (l *LevelService) UpdateLevel(clusterName string, level *levelConfig.LevelInfo) error {
	if level == nil {
		return fmt.Errorf("世界配置为空")
	}
	return l.saveLevels(clusterName, []levelConfig.LevelInfo{*level})
}

func (l *LevelService) saveLevels(clusterName string, levels []levelConfig.LevelInfo) error {
	writes, err := l.planModWrites(clusterName, levels)
	if err != nil {
		return err
	}
	config, err := l.dstConfig.GetDstConfig(clusterName)
	if err != nil {
		return err
	}
	for _, write := range writes {
		if err = fileUtils.WriterTXT(write.writePath, write.content); err != nil {
			return err
		}
	}
	seen := map[string]struct{}{}
	for _, write := range writes {
		if _, ok := seen[write.content]; ok {
			continue
		}
		seen[write.content] = struct{}{}
		if err = dstUtils.DedicatedServerModsSetup(config, write.content); err != nil {
			return fmt.Errorf("模组配置已写入，但安装列表更新失败，请刷新后核对")
		}
	}
	for i := range levels {
		if err = l.updateWorldFiles(clusterName, &levels[i]); err != nil {
			return err
		}
	}
	return nil
}

func (l *LevelService) worldFilesUnchanged(levelFolderPath string, level *levelConfig.LevelInfo) bool {
	overridePath := archive.WorldOverridePath(levelFolderPath)
	currentOverride, err := os.ReadFile(overridePath)
	if err != nil || string(currentOverride) != level.Leveldataoverride {
		return false
	}
	iniPath := filepath.Join(levelFolderPath, "server.ini")
	if _, err = os.Stat(iniPath); err != nil {
		return false
	}
	currentIni := l.GetServerIni(iniPath, level.ServerIni.IsMaster)
	return currentIni == level.ServerIni
}

func (l *LevelService) updateWorldFiles(clusterName string, level *levelConfig.LevelInfo) error {
	clusterPath := l.resolver.ClusterPath(clusterName)
	levelFolderPath := filepath.Join(clusterPath, level.Uuid)
	if err := insideCluster(clusterPath, levelFolderPath); err != nil {
		return err
	}
	fileUtils.CreateDirIfNotExists(levelFolderPath)
	if !l.worldFilesUnchanged(levelFolderPath, level) {
		l.initLevel(levelFolderPath, level, false)
	}

	saved, err := l.levelConfigUtils.GetLevelConfig(clusterName)
	if err != nil {
		return err
	}
	for i := range saved.LevelList {
		if level.Uuid == saved.LevelList[i].File {
			saved.LevelList[i].Name = level.LevelName
			break
		}
	}
	return l.levelConfigUtils.SaveLevelConfig(clusterName, saved)
}

// CreateLevel 创建新关卡
func (l *LevelService) CreateLevel(clusterName string, level *levelConfig.LevelInfo) error {
	uuid := ""
	if level.Uuid == "" {
		uuid = l.generateUUID()
	} else {
		uuid = level.Uuid
	}
	levelFolderPath := filepath.Join(l.resolver.ClusterPath(clusterName), uuid)
	fileUtils.CreateDirIfNotExists(levelFolderPath)
	l.initLevel(levelFolderPath, level, true)

	// 记录level.json 文件
	config, err := l.levelConfigUtils.GetLevelConfig(clusterName)
	if err != nil {
		return err
	}
	config.LevelList = append(config.LevelList, levelConfig.Item{Name: level.LevelName, File: uuid})
	err = l.levelConfigUtils.SaveLevelConfig(clusterName, config)
	if err != nil {
		err := fileUtils.DeleteFile(filepath.Join(l.resolver.ClusterPath(clusterName), uuid))
		return err
	}
	level.Uuid = uuid
	return nil
}

// DeleteLevel 删除关卡
func (l *LevelService) DeleteLevel(clusterName string, levelName string) error {
	// 停止关卡服务
	l.gameProcess.Stop(clusterName, levelName)

	// 删除关卡目录
	err := fileUtils.DeleteDir(filepath.Join(l.resolver.ClusterPath(clusterName), levelName))
	if err != nil {
		return err
	}

	// 删除 json 文件中的记录
	config, err := l.levelConfigUtils.GetLevelConfig(clusterName)
	if err != nil {
		log.Panicln("删除文件失败")
	}
	newLevelsConfig := levelConfig.LevelConfig{}
	for i := range config.LevelList {
		if config.LevelList[i].File != levelName {
			newLevelsConfig.LevelList = append(newLevelsConfig.LevelList, config.LevelList[i])
		}
	}
	err = l.levelConfigUtils.SaveLevelConfig(clusterName, &newLevelsConfig)

	// TODO 同时删除定时任务和自动维护

	return err
}

// initLevel 初始化关卡文件
func (l *LevelService) initLevel(levelFolderPath string, level *levelConfig.LevelInfo, writeMod bool) {

	lPath := archive.WorldOverridePath(levelFolderPath)
	mPath := filepath.Join(levelFolderPath, "modoverrides.lua")
	sPath := filepath.Join(levelFolderPath, "server.ini")

	fileUtils.CreateFileIfNotExists(lPath)
	fileUtils.CreateFileIfNotExists(sPath)
	fileUtils.WriterTXT(lPath, level.Leveldataoverride)
	if writeMod {
		fileUtils.CreateFileIfNotExists(mPath)
		fileUtils.WriterTXT(mPath, level.Modoverrides)
	}
	serverBuf := l.ParseTemplate(level.ServerIni)
	fileUtils.WriterTXT(sPath, serverBuf)
}

// ParseTemplate 解析服务器配置模板
func (l *LevelService) ParseTemplate(serverIni levelConfig.ServerIni) string {
	// 使用 gameConfig 中的 ParseTemplate 方法
	return dstUtils.ParseTemplate(gameConfig.ServerIniTemplate, serverIni)
}

// generateUUID 生成 UUID
func (l *LevelService) generateUUID() string {
	// 简化实现，实际应该使用标准的 UUID 生成库
	return "level_" + strconv.FormatInt(time.Now().UnixNano(), 10)
}
