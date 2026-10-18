package archive

import (
	"dst-admin-go/internal/pkg/utils/fileUtils"
	"dst-admin-go/internal/service/dstConfig"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
)

type PathResolver struct {
	homePath  string
	dstConfig dstConfig.Config

	// 版本查询缓存：
	// versionCache 保存 SteamCMD 权威校验结果（24h 内可信），quickVersionCache 保存快速接口结果（5 分钟）。
	versionCacheMu    sync.Mutex
	versionFetchMu    sync.Mutex
	quickFetchMu      sync.Mutex
	versionCache      map[int]dstVersionCacheEntry
	quickVersionCache map[int]dstVersionCacheEntry
}

func NewPathResolver(dstConfig dstConfig.Config) (*PathResolver, error) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		// systemd services can run without HOME set; keep the resolver usable so
		// configured persistent_storage_root paths still work.
		home = "/root"
	}

	return &PathResolver{
		homePath:          home,
		dstConfig:         dstConfig,
		versionCache:      map[int]dstVersionCacheEntry{},
		quickVersionCache: map[int]dstVersionCacheEntry{},
	}, nil
}

func (r *PathResolver) KleiBasePath(clusterName string) string {
	config, err := r.dstConfig.GetDstConfig(clusterName)
	if err != nil {
		log.Panic(err)
	}
	persistentStorageRoot := config.Persistent_storage_root
	confDir := config.Conf_dir
	if persistentStorageRoot != "" {
		if confDir == "" {
			confDir = "DoNotStarveTogether"
		}
		kleiDstPath := filepath.Join(persistentStorageRoot, confDir)
		return kleiDstPath
	}

	if runtime.GOOS == "windows" {
		return filepath.Join(
			r.homePath,
			"Documents",
			"klei",
			"DoNotStarveTogether",
		)
	}

	return filepath.Join(
		r.homePath,
		".klei",
		"DoNotStarveTogether",
	)
}

func (r *PathResolver) ClusterPath(cluster string) string {
	return filepath.Join(
		r.KleiBasePath(cluster),
		cluster,
	)
}

func (r *PathResolver) LevelPath(cluster, level string) string {
	return filepath.Join(
		r.ClusterPath(cluster),
		level,
	)
}

func (r *PathResolver) DataFilePath(
	cluster,
	level,
	fileName string,
) string {

	return filepath.Join(
		r.LevelPath(cluster, level),
		fileName,
	)
}

func (r *PathResolver) ClusterIniPath(clusterName string) string {
	return filepath.Join(r.KleiBasePath(clusterName), clusterName, "cluster.ini")
}

func (r *PathResolver) ClusterTokenPath(clusterName string) string {
	return filepath.Join(r.KleiBasePath(clusterName), clusterName, "cluster_token.txt")
}

func (r *PathResolver) AdminlistPath(clusterName string) string {
	return filepath.Join(r.KleiBasePath(clusterName), clusterName, "adminlist.txt")
}

func (r *PathResolver) BlocklistPath(clusterName string) string {
	return filepath.Join(r.KleiBasePath(clusterName), clusterName, "blocklist.txt")
}
func (r *PathResolver) BlacklistPath(clusterName string) string {
	return filepath.Join(r.KleiBasePath(clusterName), clusterName, "blocklist.txt")
}

func (r *PathResolver) WhitelistPath(clusterName string) string {
	return filepath.Join(r.KleiBasePath(clusterName), clusterName, "whitelist.txt")
}

func (r *PathResolver) ModoverridesPath(clusterName, levelName string) string {
	return filepath.Join(r.KleiBasePath(clusterName), clusterName, levelName, "modoverrides.lua")
}

func (r *PathResolver) LeveldataoverridePath(clusterName, levelName string) string {
	return WorldOverridePath(r.LevelPath(clusterName, levelName))
}

// Keep editing the override file an existing world already uses.
func WorldOverridePath(levelPath string) string {
	legacy := filepath.Join(levelPath, "leveldataoverride.lua")
	if _, err := os.Stat(legacy); err == nil {
		return legacy
	}
	worldgen := filepath.Join(levelPath, "worldgenoverride.lua")
	if _, err := os.Stat(worldgen); err == nil {
		return worldgen
	}
	return legacy
}

func (r *PathResolver) ServerIniPath(clusterName string, levelName string) string {
	return filepath.Join(r.KleiBasePath(clusterName), clusterName, levelName, "server.ini")
}

func (r *PathResolver) ServerLogPath(cluster string, levelName string) string {
	return filepath.Join(r.LevelPath(cluster, levelName), "server_log.txt")
}

func (r *PathResolver) GetUgcWorkshopModPath(clusterName, levelName, workshopId string) string {
	// dstConfig := dstConfigUtils.GetDstConfig()
	config, _ := r.dstConfig.GetDstConfig(clusterName)
	workshopModPath := ""
	if config.Ugc_directory != "" {
		workshopModPath = filepath.Join(r.GetUgcModPath(clusterName), "content", "322330", workshopId)
	} else {
		workshopModPath = filepath.Join(config.Force_install_dir, "ugc_mods", clusterName, levelName, "content", "322330", workshopId)
	}
	return workshopModPath
}

func (r *PathResolver) GetUgcModPath(clusterName string) string {
	config, _ := r.dstConfig.GetDstConfig(clusterName)
	ugcModPath := ""
	if config.Ugc_directory != "" {
		ugcModPath = config.Ugc_directory
	} else {
		ugcModPath = filepath.Join(config.Force_install_dir, "ugc_mods")
	}
	return ugcModPath
}

func (r *PathResolver) GetUgcAcfPath(clusterName, levelName string) string {
	ugcModPath := r.GetUgcModPath(clusterName)
	config, _ := r.dstConfig.GetDstConfig(clusterName)
	p := ""
	if config.Ugc_directory == "" {
		p = filepath.Join(ugcModPath, clusterName, levelName, "appworkshop_322330.acf")
	} else {
		p = filepath.Join(ugcModPath, "appworkshop_322330.acf")
	}
	return p
}

func (r *PathResolver) GetModSetup(clusterName string) string {
	cluster, _ := r.dstConfig.GetDstConfig(clusterName)
	dstServerPath := cluster.Force_install_dir
	if r.IsBeta(clusterName) {
		dstServerPath = dstServerPath + "-beta"
	}
	return filepath.Join(dstServerPath, "mods", "dedicated_server_mods_setup.lua")
}

func (r *PathResolver) IsBeta(clusterName string) bool {
	config, err := r.dstConfig.GetDstConfig(clusterName)
	if err != nil {
		return false
	}
	return config.Beta == 1
}

func (r *PathResolver) GetLocalDstVersion(clusterName string) (int64, error) {
	dstConfig, err := r.dstConfig.GetDstConfig(clusterName)
	if err != nil {
		return 0, err
	}
	dstInstallDir := dstConfig.Force_install_dir
	if dstConfig.Beta == 1 {
		dstInstallDir = dstInstallDir + "-beta"
	}
	versionTextPath := filepath.Join(dstInstallDir, "version.txt")
	return r.dstVersion(versionTextPath)
}

func (r *PathResolver) dstVersion(versionTextPath string) (int64, error) {
	// 使用filepath.Clean确保路径格式正确
	cleanPath := filepath.Clean(versionTextPath)

	// 使用filepath.Abs获取绝对路径
	absPath, err := filepath.Abs(cleanPath)
	if err != nil {
		log.Println("Error getting absolute path:", err)
		return 0, err
	}
	// 打印绝对路径
	log.Println("Absolute Path:", absPath)

	version, err := fileUtils.ReadFile(versionTextPath)
	if err != nil {
		log.Println(err)
		return 0, err
	}
	version = strings.Replace(version, "\r", "", -1)
	version = strings.Replace(version, "\n", "", -1)
	l, err := strconv.ParseInt(version, 10, 64)
	if err != nil {
		log.Println(err)
		return 0, err
	}
	return l, nil
}
