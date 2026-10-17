package archive

import (
	"context"
	"dst-admin-go/internal/config"
	"dst-admin-go/internal/pkg/utils/fileUtils"
	"dst-admin-go/internal/service/dstConfig"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	steamCmdAppID = "343050"

	// 最新版本结果缓存时间：避免每次刷新面板都运行一次 SteamCMD。
	dstVersionCacheTTL     = 5 * time.Minute
	steamCmdVersionTimeout = 25 * time.Second
	steamAPIVersionTimeout = 8 * time.Second
)

type dstVersionCacheEntry struct {
	version int64
	at      time.Time
}

// GetLastDstVersion 查询最新 DST 专用服务器 buildid。
// 依次尝试：Steam Web API（配置了 steamAPIKey 时）→ SteamCMD app_info_print（匿名）
// → 配置的 dstVersionUrl（旧版第三方地址，作为兜底）。
func (r *PathResolver) getLastDstVersion(clusterName string) (int64, error) {
	dstCfg, err := r.dstConfig.GetDstConfig(clusterName)
	if err != nil {
		return -1, err
	}
	beta := dstCfg.Beta == 1
	if cached, ok := r.cachedLatestDstVersion(beta); ok {
		return cached, nil
	}

	// 同一时间只允许一个查询，避免面板并发请求同时启动多个 SteamCMD。
	r.versionFetchMu.Lock()
	defer r.versionFetchMu.Unlock()
	if cached, ok := r.cachedLatestDstVersion(beta); ok {
		return cached, nil
	}

	var errs []string
	if apiKey := strings.TrimSpace(config.Cfg.SteamAPIKey); apiKey != "" {
		localBuildID, localErr := r.GetLocalDstBuildId(clusterName)
		if localErr == nil && localBuildID > 0 {
			version, apiErr := r.latestDstVersionViaSteamAPI(apiKey, localBuildID)
			if apiErr == nil && version > 0 {
				r.cacheLatestDstVersion(beta, version)
				log.Printf("游戏版本查询：Steam Web API 返回 buildid %d", version)
				return version, nil
			}
			if apiErr != nil {
				errs = append(errs, "Steam API: "+apiErr.Error())
			}
		} else if localErr != nil {
			errs = append(errs, "读取本地 buildid 失败: "+localErr.Error())
		}
	}

	if version, cmdErr := r.latestDstVersionViaSteamCmd(dstCfg); cmdErr == nil && version > 0 {
		r.cacheLatestDstVersion(beta, version)
		log.Printf("游戏版本查询：SteamCMD 返回 buildid %d", version)
		return version, nil
	} else if cmdErr != nil {
		errs = append(errs, "SteamCMD: "+cmdErr.Error())
	}

	return -1, fmt.Errorf("无法获取最新游戏 buildid: %s", strings.Join(errs, "; "))
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

func (r *PathResolver) cachedLatestDstVersion(beta bool) (int64, bool) {
	r.versionCacheMu.Lock()
	defer r.versionCacheMu.Unlock()
	entry, ok := r.versionCache[boolCacheKey(beta)]
	if !ok || time.Since(entry.at) > dstVersionCacheTTL {
		return 0, false
	}
	return entry.version, true
}

func (r *PathResolver) cacheLatestDstVersion(beta bool, version int64) {
	r.versionCacheMu.Lock()
	defer r.versionCacheMu.Unlock()
	if r.versionCache == nil {
		r.versionCache = map[int]dstVersionCacheEntry{}
	}
	r.versionCache[boolCacheKey(beta)] = dstVersionCacheEntry{version: version, at: time.Now()}
}

func boolCacheKey(beta bool) int {
	if beta {
		return 1
	}
	return 0
}

type upToDateCheckResponse struct {
	Response struct {
		Success         bool  `json:"success"`
		UpToDate        bool  `json:"up_to_date"`
		RequiredVersion int64 `json:"required_version"`
	} `json:"response"`
}

// latestDstVersionViaSteamAPI 使用 Steam 官方 UpToDateCheck 接口查询最新 buildid。
// 需要 config.yml 中配置 steamAPIKey；version 参数使用本地 version.txt 的 buildid。
func (r *PathResolver) latestDstVersionViaSteamAPI(apiKey string, localVersion int64) (int64, error) {
	endpoint := fmt.Sprintf(
		"https://api.steampowered.com/ISteamApps/UpToDateCheck/v1/?appid=%s&version=%d&key=%s",
		steamCmdAppID, localVersion, url.QueryEscape(apiKey),
	)
	client := &http.Client{Timeout: steamAPIVersionTimeout}
	resp, err := client.Get(endpoint)
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
	var payload upToDateCheckResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return 0, err
	}
	if !payload.Response.Success {
		return 0, fmt.Errorf("接口返回 success=false")
	}
	if payload.Response.RequiredVersion > 0 {
		return payload.Response.RequiredVersion, nil
	}
	if payload.Response.UpToDate {
		return localVersion, nil
	}
	return 0, fmt.Errorf("接口未返回 required_version")
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

// tailVersionOutput 保留输出尾部，避免错误信息过长。
func tailVersionOutput(text string, limit int) string {
	text = strings.TrimSpace(text)
	if len(text) <= limit {
		return text
	}
	return "..." + text[len(text)-limit:]
}
