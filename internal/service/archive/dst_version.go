package archive

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"dst-admin-go/internal/config"
	"dst-admin-go/internal/pkg/utils/cmdrun"
	"dst-admin-go/internal/pkg/utils/fileUtils"
	"dst-admin-go/internal/pkg/utils/steamcmd"
	"dst-admin-go/internal/service/dstConfig"
)

const (
	steamCmdAppID = "343050"

	// 快速查询（第三方）缓存时间：用于页面快速展示。
	quickVersionCacheTTL = 5 * time.Minute
	// SteamCMD 权威校验有效期：即使快速查询一直可用，也至少每天用 SteamCMD 校验一次。
	steamCmdValidateTTL = 24 * time.Hour
	// SteamCMD 查询超时。
	steamCmdVersionTimeout = 25 * time.Second
	// 快速接口超时。
	quickVersionTimeout = 6 * time.Second
	// SteamCMD 校验结果的短期复用时间，避免同一时刻重复执行。
	steamCmdResultMinAge = 10 * time.Second
	// SteamCMD 校验失败后的冷却时间：SteamCMD 不可用时避免每次页面请求都同步重试（每次最长 25s）。
	steamCmdFailureCooldown = 2 * time.Minute

	// 每日自动校验失败后的重试次数与间隔（总尝试次数 = 1 次首检 + 4 次重试）。
	dailyCheckRetryInterval = 15 * time.Minute
	dailyCheckMaxRetries    = 4
)

const (
	quickVersionURL = "https://api.steamcmd.net/v1/info/" + steamCmdAppID

	versionSourceQuick       = "quick"       // 第三方快速接口（仅用于展示/预判）
	versionSourceSteamCmd    = "steamcmd"    // 刚刚用本机 SteamCMD 校验过
	versionSourceCache       = "cache"       // 使用 24h 内的 SteamCMD 校验缓存
	versionSourceStaleCache  = "stale-cache" // SteamCMD 暂时不可用时的过期缓存
	versionSourceUnavailable = "unavailable"
)

// LatestDstVersionInfo 最新版本查询结果。
// CheckedAt 表示这份数据真正被外部来源更新/校验的时间（unix 秒），用于页面展示“检查时间”。
type LatestDstVersionInfo struct {
	Version   int64
	Source    string
	CheckedAt int64
}

type dstVersionCacheEntry struct {
	version int64
	at      time.Time
}

// GetLastDstVersion 兼容旧调用：只返回最新 buildid。
func (r *PathResolver) GetLastDstVersion(clusterName string) (int64, error) {
	info, err := r.GetLatestDstVersionInfo(clusterName)
	if err != nil {
		return -1, err
	}
	return info.Version, nil
}

// GetLatestDstVersionInfo 查询最新 DST 专用服务器 buildid（允许使用缓存）。
//
// 设计：
//  1. 快速层：api.steamcmd.net（第三方、无需认证）用于快速展示，缓存 5 分钟；
//  2. 权威层：本机 SteamCMD `app_info_print 343050`，缓存 24 小时，并且至少每天校验一次；
//  3. 两者不一致时以 SteamCMD 为准；快速层失败时使用 SteamCMD 缓存或同步查询；
//  4. 所有失败都不会被当成“已是最新”，而是返回错误/未知来源。
func (r *PathResolver) GetLatestDstVersionInfo(clusterName string) (LatestDstVersionInfo, error) {
	dstCfg, err := r.dstConfig.GetDstConfig(clusterName)
	if err != nil {
		return LatestDstVersionInfo{Version: -1, Source: versionSourceUnavailable}, err
	}
	beta := dstCfg.Beta == 1

	quickVersion, quickAt, quickOK := r.quickLatestDstVersion(beta)
	steamVersion, steamAt, steamOK := r.cachedSteamCmdVersion(beta)
	steamFresh := steamOK && time.Since(steamAt) < steamCmdValidateTTL

	// 快速检查 + SteamCMD 缓存都可用且一致：直接使用 SteamCMD 缓存（速度最快且可信）。
	if steamFresh && quickOK && quickVersion == steamVersion {
		return LatestDstVersionInfo{Version: steamVersion, Source: versionSourceCache, CheckedAt: steamAt.Unix()}, nil
	}
	// 快速检查发现与上次 SteamCMD 校验不同的版本：立即用 SteamCMD 核实，避免第三方数据错误。
	if quickOK && steamOK && quickVersion != steamVersion {
		if version, cmdErr := r.steamCmdVersionChecked(dstCfg, beta); cmdErr == nil {
			log.Printf("版本检查：快速来源 %d 与 SteamCMD 缓存 %d 不一致，以 SteamCMD %d 为准", quickVersion, steamVersion, version)
			return LatestDstVersionInfo{Version: version, Source: versionSourceSteamCmd, CheckedAt: time.Now().Unix()}, nil
		}
	}
	// 快速检查有结果（缓存为空或已过期）：先返回快速结果，同时异步触发 SteamCMD 每日校验。
	if quickOK {
		if !steamFresh {
			r.refreshSteamCmdVersionAsync(dstCfg, beta)
		}
		return LatestDstVersionInfo{Version: quickVersion, Source: versionSourceQuick, CheckedAt: quickAt.Unix()}, nil
	}
	// 快速检查不可用：使用未过期的 SteamCMD 缓存。
	if steamFresh {
		return LatestDstVersionInfo{Version: steamVersion, Source: versionSourceCache, CheckedAt: steamAt.Unix()}, nil
	}
	// 快速检查不可用、缓存也过期：同步执行 SteamCMD 校验。
	if version, cmdErr := r.steamCmdVersionChecked(dstCfg, beta); cmdErr == nil {
		return LatestDstVersionInfo{Version: version, Source: versionSourceSteamCmd, CheckedAt: time.Now().Unix()}, nil
	} else {
		log.Println("SteamCMD 版本校验失败:", cmdErr)
	}
	// 最后兜底：使用过期缓存，但标记来源为 stale-cache，避免被误判为“已是最新”。
	if steamOK {
		return LatestDstVersionInfo{Version: steamVersion, Source: versionSourceStaleCache, CheckedAt: steamAt.Unix()}, nil
	}
	return LatestDstVersionInfo{Version: -1, Source: versionSourceUnavailable}, fmt.Errorf("无法获取最新游戏 buildid")
}

// GetLatestDstVersionInfoForce 强制用本机 SteamCMD 做一次权威版本检查（手动刷新/每日定时）。
// 如果 SteamCMD 正被更新/修复任务占用，会返回错误，由调用方提示稍后重试。
func (r *PathResolver) GetLatestDstVersionInfoForce(clusterName string) (LatestDstVersionInfo, error) {
	dstCfg, err := r.dstConfig.GetDstConfig(clusterName)
	if err != nil {
		return LatestDstVersionInfo{Version: -1, Source: versionSourceUnavailable}, err
	}
	beta := dstCfg.Beta == 1
	version, err := r.latestDstVersionViaSteamCmd(dstCfg)
	if err != nil {
		r.markSteamCmdFailure(beta)
		return LatestDstVersionInfo{Version: -1, Source: versionSourceUnavailable}, err
	}
	r.clearSteamCmdFailure(beta)
	r.cacheSteamCmdVersion(beta, version)
	return LatestDstVersionInfo{Version: version, Source: versionSourceSteamCmd, CheckedAt: time.Now().Unix()}, nil
}

// InvalidateVersionCache 清理版本缓存（更新/修复完成后调用，避免页面显示旧状态）。
func (r *PathResolver) InvalidateVersionCache(clusterName string) {
	dstCfg, err := r.dstConfig.GetDstConfig(clusterName)
	if err != nil {
		return
	}
	key := boolCacheKey(dstCfg.Beta == 1)
	r.versionCacheMu.Lock()
	delete(r.versionCache, key)
	delete(r.quickVersionCache, key)
	r.versionCacheMu.Unlock()
}

// StartDailyVersionCheck 启动每日固定时间的 SteamCMD 权威版本检查（只启动一次）。
func (r *PathResolver) StartDailyVersionCheck() {
	r.dailyCheckOnce.Do(func() {
		go r.runDailyVersionCheck()
	})
}

func (r *PathResolver) runDailyVersionCheck() {
	if config.Cfg == nil {
		return
	}
	enable := true
	if config.Cfg.DailyVersionCheck.Enable != nil {
		enable = *config.Cfg.DailyVersionCheck.Enable
	}
	if !enable {
		log.Println("每日版本检查已禁用（dailyVersionCheck.enable=false）")
		return
	}
	checkTime := strings.TrimSpace(config.Cfg.DailyVersionCheck.Time)
	hour, minute, err := parseDailyCheckTime(checkTime)
	if err != nil {
		log.Println("每日版本检查时间配置无效，使用默认 04:00:", err)
		hour, minute = 4, 0
	}
	log.Printf("每日版本检查已启用：每天 %02d:%02d 使用 SteamCMD 校验一次游戏版本", hour, minute)

	// 面板在当天检查时间之后才启动时，补做一次检查（例如 10:00 重启面板，不必等到次日 04:00）。
	if r.shouldCatchUpToday(hour, minute) {
		log.Println("已过今日版本检查时间且今日尚未做过真实校验，立即补做一次")
		r.runDailyCheckWithRetries()
	}

	for {
		next := r.nextDailyCheckTime(hour, minute)
		log.Printf("下一次 SteamCMD 版本检查时间：%s", next.Format("2006-01-02 15:04:05"))
		timer := time.NewTimer(time.Until(next))
		<-timer.C
		r.runDailyCheckWithRetries()
	}
}

// nextDailyCheckTime 返回下一次每日检查时间（严格晚于当前时间）。
func (r *PathResolver) nextDailyCheckTime(hour, minute int) time.Time {
	now := time.Now()
	next := time.Date(now.Year(), now.Month(), now.Day(), hour, minute, 0, 0, now.Location())
	if !next.After(now) {
		next = next.Add(24 * time.Hour)
	}
	return next
}

// shouldCatchUpToday 判断当天计划时间已过、但今天还没有成功的 SteamCMD 校验。
func (r *PathResolver) shouldCatchUpToday(hour, minute int) bool {
	now := time.Now()
	todaySchedule := time.Date(now.Year(), now.Month(), now.Day(), hour, minute, 0, 0, now.Location())
	if now.Before(todaySchedule) {
		return false
	}
	dstCfg, err := r.dstConfig.GetDstConfig("")
	if err != nil || dstCfg.Cluster == "" {
		return false
	}
	_, checkedAt, ok := r.cachedSteamCmdVersion(dstCfg.Beta == 1)
	if !ok {
		return true
	}
	return checkedAt.Before(todaySchedule)
}

// runDailyCheckWithRetries 执行一次每日检查：1 次首检 + dailyCheckMaxRetries 次重试。
func (r *PathResolver) runDailyCheckWithRetries() {
	dstCfg, cfgErr := r.dstConfig.GetDstConfig("")
	if cfgErr != nil || dstCfg.Cluster == "" {
		log.Println("每日版本检查失败：无法获取集群配置或集群名", cfgErr)
		return
	}
	totalAttempts := 1 + dailyCheckMaxRetries
	for attempt := 1; attempt <= totalAttempts; attempt++ {
		info, checkErr := r.GetLatestDstVersionInfoForce(dstCfg.Cluster)
		if checkErr == nil {
			log.Printf("每日版本检查完成：buildid=%d，时间=%s", info.Version, time.Unix(info.CheckedAt, 0).Format("2006-01-02 15:04:05"))
			return
		}
		log.Printf("每日版本检查失败（第 %d/%d 次，其中重试 %d 次）: %v", attempt, totalAttempts, dailyCheckMaxRetries, checkErr)
		if attempt < totalAttempts {
			time.Sleep(dailyCheckRetryInterval)
		}
	}
	log.Println("每日版本检查今日已放弃，等待下一次计划时间")
}

func parseDailyCheckTime(value string) (int, int, error) {
	if value == "" {
		return 4, 0, nil
	}
	parts := strings.Split(value, ":")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("时间格式应为 HH:MM")
	}
	hour, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil || hour < 0 || hour > 23 {
		return 0, 0, fmt.Errorf("小时无效: %q", parts[0])
	}
	minute, err := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil || minute < 0 || minute > 59 {
		return 0, 0, fmt.Errorf("分钟无效: %q", parts[1])
	}
	return hour, minute, nil
}

// GetLocalDstBuildId 读取本机已安装的 DST 专用服务器 buildid。
// 来源是 Steam 的 appmanifest_343050.acf（与更新使用的 SteamCMD 数据一致）。
func (r *PathResolver) GetLocalDstBuildId(clusterName string) (int64, error) {
	dstCfg, err := r.dstConfig.GetDstConfig(clusterName)
	if err != nil {
		return 0, err
	}
	dstInstallDir := dstCfg.Force_install_dir
	if dstCfg.Beta == 1 {
		dstInstallDir = dstInstallDir + "-beta"
	}
	manifestPath := filepath.Join(dstInstallDir, "steamapps", "appmanifest_343050.acf")
	content, err := fileUtils.ReadFile(manifestPath)
	if err != nil {
		return 0, fmt.Errorf("读取 %s 失败: %w", manifestPath, err)
	}
	return parseAppManifestBuildId(content)
}

// parseAppManifestBuildId 从 appmanifest_343050.acf 中解析 buildid。
func parseAppManifestBuildId(content string) (int64, error) {
	for _, raw := range strings.Split(content, "\n") {
		key, value, ok := steamCmdKeyValue(strings.TrimSpace(raw))
		if ok && key == "buildid" {
			buildID, err := strconv.ParseInt(value, 10, 64)
			if err != nil || buildID <= 0 {
				return 0, fmt.Errorf("buildid 无效: %q", value)
			}
			return buildID, nil
		}
	}
	return 0, fmt.Errorf("appmanifest 中未找到 buildid")
}

// quickLatestDstVersion 调用第三方快速接口查询最新 buildid（缓存 5 分钟），同时返回数据获取时间。
func (r *PathResolver) quickLatestDstVersion(beta bool) (int64, time.Time, bool) {
	cacheKey := boolCacheKey(beta)
	r.versionCacheMu.Lock()
	entry, ok := r.quickVersionCache[cacheKey]
	r.versionCacheMu.Unlock()
	if ok && time.Since(entry.at) < quickVersionCacheTTL {
		return entry.version, entry.at, true
	}

	r.quickFetchMu.Lock()
	defer r.quickFetchMu.Unlock()
	// 拿到锁后再查一次缓存，避免并发重复请求。
	r.versionCacheMu.Lock()
	entry, ok = r.quickVersionCache[cacheKey]
	r.versionCacheMu.Unlock()
	if ok && time.Since(entry.at) < quickVersionCacheTTL {
		return entry.version, entry.at, true
	}

	version, err := r.latestDstVersionViaQuickAPI(beta)
	if err != nil || version <= 0 {
		if err != nil {
			log.Println("快速版本查询失败:", err)
		}
		return 0, time.Time{}, false
	}
	now := time.Now()
	r.versionCacheMu.Lock()
	if r.quickVersionCache == nil {
		r.quickVersionCache = map[int]dstVersionCacheEntry{}
	}
	r.quickVersionCache[cacheKey] = dstVersionCacheEntry{version: version, at: now}
	r.versionCacheMu.Unlock()
	return version, now, true
}

// latestDstVersionViaQuickAPI 使用 api.steamcmd.net 获取 public/updatebeta 分支 buildid。
func (r *PathResolver) latestDstVersionViaQuickAPI(beta bool) (int64, error) {
	client := &http.Client{Timeout: quickVersionTimeout}
	resp, err := client.Get(quickVersionURL)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, err
	}
	return parseQuickAPIResponse(body, beta)
}

type quickAPIResponse struct {
	Status string `json:"status"`
	Data   map[string]struct {
		Branches map[string]struct {
			BuildID string `json:"buildid"`
		} `json:"branches"`
	} `json:"data"`
}

// parseQuickAPIResponse 从 steamcmd.net 返回的 JSON 中解析指定分支 buildid。
func parseQuickAPIResponse(body []byte, beta bool) (int64, error) {
	var payload quickAPIResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return 0, err
	}
	if payload.Status != "success" {
		return 0, fmt.Errorf("接口状态不是 success: %q", payload.Status)
	}
	app, ok := payload.Data[steamCmdAppID]
	if !ok {
		return 0, fmt.Errorf("返回数据中没有 appid %s", steamCmdAppID)
	}
	branchName := "public"
	if beta {
		branchName = "updatebeta"
	}
	branch, ok := app.Branches[branchName]
	if !ok {
		return 0, fmt.Errorf("返回数据中没有 %s 分支", branchName)
	}
	buildID, err := strconv.ParseInt(branch.BuildID, 10, 64)
	if err != nil || buildID <= 0 {
		return 0, fmt.Errorf("分支 buildid 无效: %q", branch.BuildID)
	}
	return buildID, nil
}

// cachedSteamCmdVersion 读取 SteamCMD 校验缓存（不限新鲜度，由调用方判断）。
func (r *PathResolver) cachedSteamCmdVersion(beta bool) (int64, time.Time, bool) {
	r.versionCacheMu.Lock()
	defer r.versionCacheMu.Unlock()
	entry, ok := r.versionCache[boolCacheKey(beta)]
	if !ok {
		return 0, time.Time{}, false
	}
	return entry.version, entry.at, true
}

func (r *PathResolver) cacheSteamCmdVersion(beta bool, version int64) {
	r.versionCacheMu.Lock()
	defer r.versionCacheMu.Unlock()
	if r.versionCache == nil {
		r.versionCache = map[int]dstVersionCacheEntry{}
	}
	r.versionCache[boolCacheKey(beta)] = dstVersionCacheEntry{version: version, at: time.Now()}
}

// steamCmdFailureBlocked 返回该分支是否处于 SteamCMD 校验失败冷却期。
func (r *PathResolver) steamCmdFailureBlocked(beta bool) bool {
	r.steamFailMu.Lock()
	defer r.steamFailMu.Unlock()
	failedAt, ok := r.steamFailAt[boolCacheKey(beta)]
	return ok && time.Since(failedAt) < steamCmdFailureCooldown
}

// markSteamCmdFailure 记录一次 SteamCMD 校验失败，冷却期内不再同步重试。
func (r *PathResolver) markSteamCmdFailure(beta bool) {
	r.steamFailMu.Lock()
	defer r.steamFailMu.Unlock()
	if r.steamFailAt == nil {
		r.steamFailAt = map[int]time.Time{}
	}
	r.steamFailAt[boolCacheKey(beta)] = time.Now()
}

// clearSteamCmdFailure 校验成功后清除失败冷却。
func (r *PathResolver) clearSteamCmdFailure(beta bool) {
	r.steamFailMu.Lock()
	defer r.steamFailMu.Unlock()
	delete(r.steamFailAt, boolCacheKey(beta))
}

// steamCmdVersionChecked 同步执行一次 SteamCMD 校验并更新缓存。
func (r *PathResolver) steamCmdVersionChecked(dstCfg dstConfig.DstConfig, beta bool) (int64, error) {
	r.versionFetchMu.Lock()
	defer r.versionFetchMu.Unlock()
	// 拿到锁后若刚校验过则直接复用，避免并发重复启动 SteamCMD。
	if version, at, ok := r.cachedSteamCmdVersion(beta); ok && time.Since(at) < steamCmdResultMinAge {
		return version, nil
	}
	// SteamCMD 刚失败过则不再同步重试：页面加载/存档页等非强制入口不应因此阻塞 25s。
	if r.steamCmdFailureBlocked(beta) {
		return 0, fmt.Errorf("SteamCMD 最近校验失败（%s 内不重复尝试），请稍后再试", steamCmdFailureCooldown)
	}
	version, err := r.latestDstVersionViaSteamCmd(dstCfg)
	if err != nil {
		r.markSteamCmdFailure(beta)
		return 0, err
	}
	r.clearSteamCmdFailure(beta)
	r.cacheSteamCmdVersion(beta, version)
	return version, nil
}

// refreshSteamCmdVersionAsync 后台执行 SteamCMD 校验，不阻塞页面。
func (r *PathResolver) refreshSteamCmdVersionAsync(dstCfg dstConfig.DstConfig, beta bool) {
	go func() {
		if !r.versionFetchMu.TryLock() {
			return
		}
		defer r.versionFetchMu.Unlock()
		if _, at, ok := r.cachedSteamCmdVersion(beta); ok && time.Since(at) < steamCmdValidateTTL {
			return
		}
		if r.steamCmdFailureBlocked(beta) {
			return
		}
		version, err := r.latestDstVersionViaSteamCmd(dstCfg)
		if err != nil {
			r.markSteamCmdFailure(beta)
			log.Println("后台 SteamCMD 版本校验失败:", err)
			return
		}
		r.clearSteamCmdFailure(beta)
		r.cacheSteamCmdVersion(beta, version)
		log.Printf("后台 SteamCMD 版本校验完成：buildid %d", version)
	}()
}

// latestDstVersionViaSteamCmd 使用 SteamCMD 的 app_info_print 查询最新 buildid（匿名可用）。
func (r *PathResolver) latestDstVersionViaSteamCmd(dstCfg dstConfig.DstConfig) (int64, error) {
	steamCmdPath := strings.TrimSpace(dstCfg.Steamcmd)
	if steamCmdPath == "" {
		return 0, fmt.Errorf("未配置 SteamCMD 路径")
	}
	binary := filepath.Join(steamCmdPath, "steamcmd")
	if runtime.GOOS == "windows" {
		binary = filepath.Join(steamCmdPath, "steamcmd.exe")
	} else if fileUtils.Exists(filepath.Join(steamCmdPath, "steamcmd.sh")) {
		binary = filepath.Join(steamCmdPath, "steamcmd.sh")
	}
	if !fileUtils.Exists(binary) {
		return 0, fmt.Errorf("未找到 SteamCMD: %s", binary)
	}
	// 与更新/修复共用 SteamCMD 锁；忙时直接返回错误，避免两个 SteamCMD 实例互相干扰。
	if !steamcmd.TryLock() {
		return 0, fmt.Errorf("SteamCMD 正在执行其他任务，请稍后再试")
	}
	defer steamcmd.Unlock()

	// 超时必须终止整个进程组：steamcmd.sh 下的 steamcmd 是孙进程，
	// 只杀 direct child 会让检查实际不返回或残留进程继续占着 SteamCMD 目录。
	output, err := cmdrun.Run(
		context.Background(),
		steamCmdVersionTimeout,
		steamCmdPath,
		binary,
		"+login", "anonymous", "+app_info_print", steamCmdAppID, "+quit",
	)
	if err != nil {
		if errors.Is(err, cmdrun.ErrTimeout) {
			return 0, fmt.Errorf("查询超时（%s）", steamCmdVersionTimeout)
		}
		return 0, fmt.Errorf("执行失败: %v，输出尾部: %s", err, tailVersionOutput(output, 300))
	}
	return parseSteamCmdBuildId(output, dstCfg.Beta == 1)
}

// parseSteamCmdBuildId 从 `steamcmd +app_info_print 343050` 输出中解析指定分支的 buildid。
// beta=true 时取 updatebeta 分支，否则取 public 分支。
func parseSteamCmdBuildId(output string, beta bool) (int64, error) {
	targetBranch := "public"
	if beta {
		targetBranch = "updatebeta"
	}
	lines := strings.Split(output, "\n")
	inBranches := false
	depth := 0
	currentBranch := ""
	foundBranches := false

	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		if !inBranches {
			if line == `"branches"` {
				inBranches = true
				foundBranches = true
				depth = 0
				currentBranch = ""
			}
			continue
		}
		switch line {
		case "{":
			depth++
			continue
		case "}":
			depth--
			if depth <= 0 {
				inBranches = false
				currentBranch = ""
			}
			continue
		}
		key, value, ok := steamCmdKeyValue(line)
		if !ok {
			continue
		}
		if depth == 1 && value == "" {
			currentBranch = key
			continue
		}
		if depth >= 2 && key == "buildid" && currentBranch == targetBranch {
			buildID, err := strconv.ParseInt(value, 10, 64)
			if err != nil || buildID <= 0 {
				return 0, fmt.Errorf("buildid 无效: %q", value)
			}
			return buildID, nil
		}
	}

	if !foundBranches {
		return 0, fmt.Errorf("输出中没有 branches 信息")
	}
	return 0, fmt.Errorf("输出中未找到 %s 分支的 buildid", targetBranch)
}

// steamCmdKeyValue 解析 SteamCMD 输出中的 `"key"	"value"` 或单独一行 `"key"`。
func steamCmdKeyValue(line string) (key, value string, ok bool) {
	fields := strings.Fields(line)
	if len(fields) == 0 || !strings.HasPrefix(fields[0], `"`) {
		return "", "", false
	}
	trimQuotes := func(token string) string {
		return strings.Trim(strings.TrimSpace(token), `"`)
	}
	if len(fields) == 1 {
		if strings.Count(fields[0], `"`) != 2 {
			return "", "", false
		}
		return trimQuotes(fields[0]), "", true
	}
	return trimQuotes(fields[0]), trimQuotes(strings.Join(fields[1:], " ")), true
}

func boolCacheKey(beta bool) int {
	if beta {
		return 1
	}
	return 0
}

// tailVersionOutput 保留输出尾部，避免错误信息过长。
func tailVersionOutput(text string, limit int) string {
	text = strings.TrimSpace(text)
	if len(text) <= limit {
		return text
	}
	return "..." + text[len(text)-limit:]
}
