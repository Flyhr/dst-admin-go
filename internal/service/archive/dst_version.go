package archive

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"dst-admin-go/internal/pkg/utils/fileUtils"
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
)

const (
	quickVersionURL = "https://api.steamcmd.net/v1/info/" + steamCmdAppID

	versionSourceQuick       = "quick"       // 第三方快速接口（仅用于展示/预判）
	versionSourceSteamCmd    = "steamcmd"    // 刚刚用本机 SteamCMD 校验过
	versionSourceCache       = "cache"       // 使用 24h 内的 SteamCMD 校验缓存
	versionSourceStaleCache  = "stale-cache" // SteamCMD 暂时不可用时的过期缓存
	versionSourceUnavailable = "unavailable"
)

// LatestDstVersionInfo 最新版本查询结果，附带来源与最近一次 SteamCMD 校验时间。
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

// GetLatestDstVersionInfo 查询最新 DST 专用服务器 buildid。
//
// 设计：
//  1. 快速层：api.steamcmd.net（第三方、无需认证）用于快速展示，缓存 5 分钟；
//  2. 权威层：本机 SteamCMD `app_info_print 343050`，结果缓存 24 小时，并且至少每天校验一次；
//  3. 两者不一致时以 SteamCMD 为准；快速层失败时使用 SteamCMD 缓存或同步查询；
//  4. 所有失败都不会被当成“已是最新”，而是返回错误/未知来源。
func (r *PathResolver) GetLatestDstVersionInfo(clusterName string) (LatestDstVersionInfo, error) {
	dstCfg, err := r.dstConfig.GetDstConfig(clusterName)
	if err != nil {
		return LatestDstVersionInfo{Version: -1, Source: versionSourceUnavailable}, err
	}
	beta := dstCfg.Beta == 1

	quickVersion, quickOK := r.quickLatestDstVersion(beta)
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
		checkedAt := int64(0)
		if steamOK {
			checkedAt = steamAt.Unix()
		}
		return LatestDstVersionInfo{Version: quickVersion, Source: versionSourceQuick, CheckedAt: checkedAt}, nil
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

// quickLatestDstVersion 调用第三方快速接口查询最新 buildid（缓存 5 分钟）。
func (r *PathResolver) quickLatestDstVersion(beta bool) (int64, bool) {
	cacheKey := boolCacheKey(beta)
	r.versionCacheMu.Lock()
	entry, ok := r.quickVersionCache[cacheKey]
	r.versionCacheMu.Unlock()
	if ok && time.Since(entry.at) < quickVersionCacheTTL {
		return entry.version, true
	}

	r.quickFetchMu.Lock()
	defer r.quickFetchMu.Unlock()
	// 拿到锁后再查一次缓存，避免并发重复请求。
	r.versionCacheMu.Lock()
	entry, ok = r.quickVersionCache[cacheKey]
	r.versionCacheMu.Unlock()
	if ok && time.Since(entry.at) < quickVersionCacheTTL {
		return entry.version, true
	}

	version, err := r.latestDstVersionViaQuickAPI(beta)
	if err != nil || version <= 0 {
		if err != nil {
			log.Println("快速版本查询失败:", err)
		}
		return 0, false
	}
	r.versionCacheMu.Lock()
	if r.quickVersionCache == nil {
		r.quickVersionCache = map[int]dstVersionCacheEntry{}
	}
	r.quickVersionCache[cacheKey] = dstVersionCacheEntry{version: version, at: time.Now()}
	r.versionCacheMu.Unlock()
	return version, true
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

// steamCmdVersionChecked 同步执行一次 SteamCMD 校验并更新缓存。
func (r *PathResolver) steamCmdVersionChecked(dstCfg dstConfig.DstConfig, beta bool) (int64, error) {
	r.versionFetchMu.Lock()
	defer r.versionFetchMu.Unlock()
	// 拿到锁后若刚校验过则直接复用，避免并发重复启动 SteamCMD。
	if version, at, ok := r.cachedSteamCmdVersion(beta); ok && time.Since(at) < steamCmdResultMinAge {
		return version, nil
	}
	version, err := r.latestDstVersionViaSteamCmd(dstCfg)
	if err != nil {
		return 0, err
	}
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
		version, err := r.latestDstVersionViaSteamCmd(dstCfg)
		if err != nil {
			log.Println("后台 SteamCMD 版本校验失败:", err)
			return
		}
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

	ctx, cancel := context.WithTimeout(context.Background(), steamCmdVersionTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "+login", "anonymous", "+app_info_print", steamCmdAppID, "+quit")
	cmd.Dir = steamCmdPath
	output, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return 0, fmt.Errorf("查询超时（%s）", steamCmdVersionTimeout)
	}
	if err != nil {
		return 0, fmt.Errorf("执行失败: %v，输出尾部: %s", err, tailVersionOutput(string(output), 300))
	}
	return parseSteamCmdBuildId(string(output), dstCfg.Beta == 1)
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
