package mod

import (
	"dst-admin-go/internal/model"
	"dst-admin-go/internal/service/dstConfig"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	// modInstallTimeout 等待游戏安装模组的最长时间，超过后未安装的模组转为失败状态。
	modInstallTimeout = 3 * time.Minute
	// modInstallKVKeyPrefix KV 中保存模组安装跟踪记录的 key 前缀，完整 key 为 mod-install:<cluster>。
	modInstallKVKeyPrefix = "mod-install:"
)

// ModInstallTracking 持久化到 KV 的安装跟踪记录。
type ModInstallTracking struct {
	StartedAt int64    `json:"startedAt"`
	Ids       []string `json:"ids"`
}

// ModInstallState 单个模组的轻量状态（不读 modinfo 内容，只看文件是否存在）。
type ModInstallState struct {
	Installed  bool `json:"installed"`
	Downloaded bool `json:"downloaded"`
}

// ModInstallStatus 轻量安装状态接口返回结构。
type ModInstallStatus struct {
	Running        bool                       `json:"running"`
	TimedOut       bool                       `json:"timedOut"`
	StartedAt      int64                      `json:"startedAt"`
	TimeoutSeconds int                        `json:"timeoutSeconds"`
	Total          int                        `json:"total"`
	Done           int                        `json:"done"`
	Pending        []string                   `json:"pending"`
	Failed         []string                   `json:"failed"`
	Mods           map[string]ModInstallState `json:"mods"`
}

// serverModSetupPattern 匹配 dedicated_server_mods_setup.lua 中的 ServerModSetup("id")。
var serverModSetupPattern = regexp.MustCompile(`ServerModSetup\s*\(\s*["']?(\d+)["']?\s*\)`)

func modInstallKVKey(clusterName string) string {
	return modInstallKVKeyPrefix + clusterName
}

// BeginInstallTracking 游戏启动成功后登记本次需要等待安装的模组。
// 只跟踪「面板缓存已有 modinfo ∩ setup 下载清单中 ∩ 游戏尚未安装」的模组，
// 客户端模组等游戏永远装不上的模组不会被算进等待列表。
func (s *ModService) BeginInstallTracking(clusterName string) error {
	return s.beginInstallTrackingAt(clusterName, time.Now())
}

// beginInstallTrackingAt 与 BeginInstallTracking 相同，但允许注入当前时间便于测试。
func (s *ModService) beginInstallTrackingAt(clusterName string, now time.Time) error {
	config, clusterName, err := s.resolveModInstallContext(clusterName)
	if err != nil {
		return err
	}
	candidates, err := s.pendingInstallCandidates(clusterName, config)
	if err != nil {
		return err
	}
	key := modInstallKVKey(clusterName)
	if len(candidates) == 0 {
		// 没有需要等待的模组：清除上一次的跟踪记录，避免刷新后一直显示等待。
		return s.deleteModInstallTracking(key)
	}
	value, err := json.Marshal(ModInstallTracking{StartedAt: now.Unix(), Ids: candidates})
	if err != nil {
		return fmt.Errorf("序列化模组安装跟踪记录失败: %w", err)
	}
	return s.saveModInstallTracking(key, string(value))
}

// GetModInstallStatus 返回轻量安装状态；只扫描文件路径，不调用 Steam API、不读 modinfo 内容。
func (s *ModService) GetModInstallStatus(clusterName string) (*ModInstallStatus, error) {
	config, clusterName, err := s.resolveModInstallContext(clusterName)
	if err != nil {
		return nil, err
	}
	installedIDs, err := s.installedModIDs(clusterName, config)
	if err != nil {
		return nil, fmt.Errorf("扫描已安装模组失败: %w", err)
	}
	cachedIDs, err := s.cachedModIDs(config)
	if err != nil {
		return nil, fmt.Errorf("扫描面板缓存模组失败: %w", err)
	}
	setupIDs, err := s.setupModIDs(clusterName)
	if err != nil {
		return nil, fmt.Errorf("读取模组下载清单失败: %w", err)
	}
	tracking, hasTracking, err := s.getModInstallTracking(clusterName)
	if err != nil {
		return nil, fmt.Errorf("读取模组安装跟踪记录失败: %w", err)
	}

	status := &ModInstallStatus{
		TimeoutSeconds: int(modInstallTimeout / time.Second),
		Pending:        []string{},
		Failed:         []string{},
		Mods:           make(map[string]ModInstallState),
	}

	knownIDs := make(map[string]bool)
	for id := range installedIDs {
		knownIDs[id] = true
	}
	for id := range cachedIDs {
		knownIDs[id] = true
	}
	for id := range setupIDs {
		knownIDs[id] = true
	}
	for _, id := range tracking.Ids {
		knownIDs[id] = true
	}
	for id := range knownIDs {
		status.Mods[id] = ModInstallState{
			Installed:  installedIDs[id],
			Downloaded: installedIDs[id] || cachedIDs[id],
		}
	}

	if !hasTracking {
		return status, nil
	}

	status.StartedAt = tracking.StartedAt
	status.Total = len(tracking.Ids)
	installedCount := 0
	pending := make([]string, 0, len(tracking.Ids))
	for _, id := range tracking.Ids {
		if installedIDs[id] {
			installedCount++
			continue
		}
		pending = append(pending, id)
	}
	status.Done = installedCount

	if len(pending) > 0 && time.Since(time.Unix(tracking.StartedAt, 0)) > modInstallTimeout {
		// 超时仍未安装：转为失败状态，等待前端提示并允许用户重新检查。
		status.TimedOut = true
		status.Failed = pending
		return status, nil
	}
	status.Pending = pending
	status.Running = len(pending) > 0
	return status, nil
}

// resolveModInstallContext 读取 DST 配置并补全集群名。
func (s *ModService) resolveModInstallContext(clusterName string) (dstConfig.DstConfig, string, error) {
	config, err := s.dstConfig.GetDstConfig(clusterName)
	if err != nil {
		return dstConfig.DstConfig{}, clusterName, fmt.Errorf("获取DST配置失败: %w", err)
	}
	if clusterName == "" {
		clusterName = config.Cluster
	}
	return config, clusterName, nil
}

// pendingInstallCandidates 计算需要跟踪的候选模组：缓存 ∩ setup − 已安装。
func (s *ModService) pendingInstallCandidates(clusterName string, config dstConfig.DstConfig) ([]string, error) {
	cachedIDs, err := s.cachedModIDs(config)
	if err != nil {
		return nil, fmt.Errorf("扫描面板缓存模组失败: %w", err)
	}
	setupIDs, err := s.setupModIDs(clusterName)
	if err != nil {
		return nil, fmt.Errorf("读取模组下载清单失败: %w", err)
	}
	installedIDs, err := s.installedModIDs(clusterName, config)
	if err != nil {
		return nil, fmt.Errorf("扫描已安装模组失败: %w", err)
	}
	candidates := make([]string, 0, len(cachedIDs))
	for id := range cachedIDs {
		if setupIDs[id] && !installedIDs[id] {
			candidates = append(candidates, id)
		}
	}
	sort.Strings(candidates)
	return candidates, nil
}

// installedModIDs 扫描游戏已安装的模组 id，路径规则与 mergeInstalledMods 保持一致。
func (s *ModService) installedModIDs(clusterName string, config dstConfig.DstConfig) (map[string]bool, error) {
	ids := make(map[string]bool)
	var patterns []string
	if config.Ugc_directory != "" {
		patterns = append(patterns, filepath.Join(s.pathResolver.GetUgcModPath(clusterName), "content", "322330", "*", "modinfo.lua"))
	} else if config.Force_install_dir != "" && clusterName != "" {
		patterns = append(patterns, filepath.Join(s.pathResolver.GetUgcModPath(clusterName), clusterName, "*", "content", "322330", "*", "modinfo.lua"))
	}
	if config.Force_install_dir != "" {
		patterns = append(patterns, filepath.Join(config.Force_install_dir, "mods", "workshop-*", "modinfo.lua"))
	}
	for _, pattern := range patterns {
		files, err := filepath.Glob(pattern)
		if err != nil {
			return nil, err
		}
		for _, path := range files {
			id := strings.TrimPrefix(filepath.Base(filepath.Dir(path)), "workshop-")
			if isWorkshopId(id) {
				ids[id] = true
			}
		}
	}
	return ids, nil
}

// cachedModIDs 扫描面板下载缓存里已有 modinfo.lua 的模组 id。
func (s *ModService) cachedModIDs(config dstConfig.DstConfig) (map[string]bool, error) {
	ids := make(map[string]bool)
	if config.Mod_download_path == "" {
		return ids, nil
	}
	pattern := filepath.Join(config.Mod_download_path, "steamapps", "workshop", "content", "322330", "*", "modinfo.lua")
	files, err := filepath.Glob(pattern)
	if err != nil {
		return nil, err
	}
	for _, path := range files {
		id := filepath.Base(filepath.Dir(path))
		if isWorkshopId(id) {
			ids[id] = true
		}
	}
	return ids, nil
}

// setupModIDs 读取 dedicated_server_mods_setup.lua 中 ServerModSetup 的模组 id。
func (s *ModService) setupModIDs(clusterName string) (map[string]bool, error) {
	ids := make(map[string]bool)
	content, err := os.ReadFile(s.pathResolver.GetModSetup(clusterName))
	if err != nil {
		if os.IsNotExist(err) {
			return ids, nil
		}
		return nil, err
	}
	for _, match := range serverModSetupPattern.FindAllStringSubmatch(string(content), -1) {
		if len(match) > 1 && isWorkshopId(match[1]) {
			ids[match[1]] = true
		}
	}
	return ids, nil
}

// getModInstallTracking 读取 KV 中的安装跟踪记录；无记录或记录损坏时返回 false。
func (s *ModService) getModInstallTracking(clusterName string) (ModInstallTracking, bool, error) {
	record, exists, err := s.findModInstallKV(modInstallKVKey(clusterName))
	if err != nil || !exists {
		return ModInstallTracking{}, false, err
	}
	if strings.TrimSpace(record.Value) == "" {
		return ModInstallTracking{}, false, nil
	}
	var tracking ModInstallTracking
	if err := json.Unmarshal([]byte(record.Value), &tracking); err != nil {
		return ModInstallTracking{}, false, nil
	}
	if tracking.StartedAt == 0 || len(tracking.Ids) == 0 {
		return ModInstallTracking{}, false, nil
	}
	return tracking, true, nil
}

// findModInstallKV 查询 KV 记录；使用 Find 而不是 First，避免状态接口轮询时反复打印 record not found。
func (s *ModService) findModInstallKV(key string) (model.KV, bool, error) {
	var records []model.KV
	if err := s.db.Where("key = ?", key).Limit(1).Find(&records).Error; err != nil {
		return model.KV{}, false, err
	}
	if len(records) == 0 {
		return model.KV{}, false, nil
	}
	return records[0], true, nil
}

// saveModInstallTracking 写入 KV 安装跟踪记录（不存在则创建，存在则更新）。
func (s *ModService) saveModInstallTracking(key, value string) error {
	record, exists, err := s.findModInstallKV(key)
	if err != nil {
		return err
	}
	if !exists {
		return s.db.Create(&model.KV{Key: key, Value: value}).Error
	}
	record.Value = value
	return s.db.Save(&record).Error
}

// deleteModInstallTracking 删除 KV 安装跟踪记录（硬删除，避免软删除记录干扰后续写入）。
func (s *ModService) deleteModInstallTracking(key string) error {
	return s.db.Unscoped().Where("key = ?", key).Delete(&model.KV{}).Error
}
