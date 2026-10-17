package mod

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/tls"
	"dst-admin-go/internal/model"
	"dst-admin-go/internal/pkg/utils/fileUtils"
	"dst-admin-go/internal/pkg/utils/shellUtils"
	"dst-admin-go/internal/service/archive"
	"dst-admin-go/internal/service/dstConfig"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"math"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	lua "github.com/yuin/gopher-lua"
	"gorm.io/gorm"
)

const (
	steamAPIKey = "73DF9F781D195DFD3D19DED1CB72EEE6"
	appID       = 322330
	language    = 6

	// modDownloadTimeout 限制单个模组的 SteamCMD 下载时间，避免请求无限阻塞。
	modDownloadTimeout = 10 * time.Minute
	// steamCmdOutputTailLimit 错误信息中保留的 SteamCMD 输出尾部字符数。
	steamCmdOutputTailLimit = 500
)

func steamAuthorProfileURL(steamID string) string {
	if steamID == "" {
		return ""
	}
	return fmt.Sprintf("https://steamcommunity.com/profiles/%s/", steamID)
}

func steamIDFromAuthor(author string) string {
	author = strings.TrimSpace(author)
	if author == "" {
		return ""
	}
	if matched, _ := regexp.MatchString(`^\d{17}$`, author); matched {
		return author
	}
	re := regexp.MustCompile(`profiles/(\d{17})`)
	matches := re.FindStringSubmatch(author)
	if len(matches) > 1 {
		return matches[1]
	}
	return ""
}

func fetchSteamPersonaNames(steamIDs []string) map[string]string {
	result := make(map[string]string)
	seen := make(map[string]bool)
	unique := make([]string, 0, len(steamIDs))
	for _, steamID := range steamIDs {
		steamID = steamIDFromAuthor(steamID)
		if steamID == "" || seen[steamID] {
			continue
		}
		seen[steamID] = true
		unique = append(unique, steamID)
	}
	if len(unique) == 0 {
		return result
	}

	client := &http.Client{Timeout: 8 * time.Second}
	for i := 0; i < len(unique); i += 100 {
		end := i + 100
		if end > len(unique) {
			end = len(unique)
		}
		data := url.Values{}
		data.Set("key", steamAPIKey)
		data.Set("steamids", strings.Join(unique[i:end], ","))
		urlStr := "https://api.steampowered.com/ISteamUser/GetPlayerSummaries/v2/?" + data.Encode()
		resp, err := client.Get(urlStr)
		if err != nil {
			continue
		}
		func() {
			defer resp.Body.Close()
			var payload struct {
				Response struct {
					Players []struct {
						SteamID     string `json:"steamid"`
						PersonaName string `json:"personaname"`
					} `json:"players"`
				} `json:"response"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
				return
			}
			for _, player := range payload.Response.Players {
				if player.SteamID != "" && player.PersonaName != "" {
					result[player.SteamID] = player.PersonaName
				}
			}
		}()
	}
	return result
}

func steamPersonaName(steamID string) string {
	steamID = steamIDFromAuthor(steamID)
	if steamID == "" {
		return ""
	}
	if names := fetchSteamPersonaNames([]string{steamID}); names[steamID] != "" {
		return names[steamID]
	}
	return steamID
}

type ModService struct {
	db           *gorm.DB
	dstConfig    dstConfig.Config
	pathResolver *archive.PathResolver
	// modDownloadLocks 按 modId 串行化模组下载，避免并发安装同一模组时重复下载。
	modDownloadLocks sync.Map
}

func NewModService(db *gorm.DB, config dstConfig.Config, pathResolver *archive.PathResolver) *ModService {
	return &ModService{
		db:           db,
		dstConfig:    config,
		pathResolver: pathResolver,
	}
}

// SearchResult 搜索结果
type SearchResult struct {
	Page      int       `json:"page"`
	Size      int       `json:"size"`
	Total     int       `json:"total"`
	TotalPage int       `json:"totalPage"`
	Data      []ModInfo `json:"data"`
}

// ModInfo 搜索返回的mod信息
type ModInfo struct {
	ID            string  `json:"id"`
	Name          string  `json:"name"`
	Author        string  `json:"author"`
	Desc          string  `json:"desc"`
	Time          int     `json:"time"`
	Sub           int     `json:"sub"`
	Img           string  `json:"img"`
	FileUrl       string  `json:"file_url"`
	V             string  `json:"v"`
	LastTime      float64 `json:"last_time"`
	ConsumerAppid float64 `json:"consumer_appid"`
	CreatorAppid  float64 `json:"creator_appid"`
	Vote          struct {
		Star int `json:"star"`
		Num  int `json:"num"`
	} `json:"vote"`
	Child []string `json:"child,omitempty"`
}

// Publishedfiledetail Steam API 返回的模组详情
type Publishedfiledetail struct {
	Publishedfileid       string  `json:"publishedfileid"`
	Result                int     `json:"result"`
	Creator               string  `json:"creator"`
	CreatorAppID          int     `json:"creator_app_id"`
	ConsumerAppID         int     `json:"consumer_app_id"`
	Filename              string  `json:"filename"`
	FileURL               string  `json:"file_url"`
	HcontentFile          string  `json:"hcontent_file"`
	PreviewURL            string  `json:"preview_url"`
	HcontentPreview       string  `json:"hcontent_preview"`
	Title                 string  `json:"title"`
	Description           string  `json:"description"`
	TimeCreated           float64 `json:"time_created"`
	TimeUpdated           float64 `json:"time_updated"`
	Visibility            int     `json:"visibility"`
	BanReason             string  `json:"ban_reason"`
	Subscriptions         int     `json:"subscriptions"`
	Favorited             int     `json:"favorited"`
	LifetimeSubscriptions int     `json:"lifetime_subscriptions"`
	LifetimeFavorited     int     `json:"lifetime_favorited"`
	Views                 int     `json:"views"`
	Tags                  []struct {
		Tag string `json:"tag"`
	} `json:"tags"`
}

// WorkshopItemDetail UGC mod详情
type WorkshopItemDetail struct {
	WorkShopId  string  `json:"workshopId"`
	Name        string  `json:"name"`
	Timeupdated int64   `json:"timeupdated"`
	Timelast    float64 `json:"timelast"`
	Img         string  `json:"img"`
}

// WorkshopItem ACF文件中的Workshop项
type WorkshopItem struct {
	TimeUpdated int64
	Manifest    string
	Ugchandle   string
}

// SearchModList 搜索模组列表
func (s *ModService) SearchModList(text string, page, size int, lang string) (*SearchResult, error) {
	// 判断是否是modID搜索
	modId, ok := isModId(text)
	if ok {
		modInfo := s.searchModInfoByWorkshopId(modId)
		data := []ModInfo{}
		if modInfo.ID != "" {
			data = append(data, modInfo)
		}
		return &SearchResult{
			Page:      1,
			Size:      1,
			Total:     1,
			TotalPage: 1,
			Data:      data,
		}, nil
	}

	// 调用 Steam API 搜索
	urlStr := "http://api.steampowered.com/IPublishedFileService/QueryFiles/v1/"
	data := url.Values{
		"page":             {fmt.Sprintf("%d", page)},
		"key":              {steamAPIKey},
		"appid":            {"322330"},
		"language":         {"6"},
		"return_tags":      {"true"},
		"numperpage":       {fmt.Sprintf("%d", size)},
		"search_text":      {text},
		"return_vote_data": {"true"},
		"return_children":  {"true"},
	}
	if lang == "zh" {
		data.Set("language", "6")
	} else {
		data.Set("language", "")
	}
	urlStr = urlStr + "?" + data.Encode()

	var modData map[string]interface{}
	for i := 0; i < 2; i++ {
		resp, err := http.Get(urlStr)
		if err != nil {
			return nil, fmt.Errorf("搜索mod失败: %w", err)
		}
		defer resp.Body.Close()
		err = json.NewDecoder(resp.Body).Decode(&modData)
		if err != nil {
			return nil, fmt.Errorf("解析mod数据失败: %w", err)
		}
		if modData["response"] != nil {
			break
		}
	}

	if modData["response"] == nil {
		return nil, errors.New("no response found in mod data")
	}

	modResponse := modData["response"].(map[string]interface{})
	total := int(modResponse["total"].(float64))
	modInfoRaw := modResponse["publishedfiledetails"].([]interface{})

	modList := make([]ModInfo, 0)
	if len(modInfoRaw) > 0 {
		for _, modInfoRaw := range modInfoRaw {
			modInfo := modInfoRaw.(map[string]interface{})
			img := modInfo["preview_url"].(string)
			voteData := modInfo["vote_data"].(map[string]interface{})
			auth := fmt.Sprintf("%v", modInfo["creator"])
			mod := ModInfo{
				ID:     fmt.Sprintf("%v", modInfo["publishedfileid"]),
				Name:   fmt.Sprintf("%v", modInfo["title"]),
				Author: auth,
				Desc:   fmt.Sprintf("%v", modInfo["file_description"]),
				Time:   int(modInfo["time_updated"].(float64)),
				Sub:    int(modInfo["subscriptions"].(float64)),
				Img:    img,
				Vote: struct {
					Star int `json:"star"`
					Num  int `json:"num"`
				}{
					Star: int(voteData["score"].(float64)*5) + 1,
					Num:  int(voteData["votes_up"].(float64) + voteData["votes_down"].(float64)),
				},
			}
			if modInfo["num_children"].(float64) != 0 {
				children := modInfo["children"].([]interface{})
				child := make([]string, len(children))
				for i, c := range children {
					child[i] = fmt.Sprintf("%v", c.(map[string]interface{})["publishedfileid"])
				}
				mod.Child = child
			}
			modList = append(modList, mod)
		}
	}

	authorIDs := make([]string, 0, len(modList))
	for _, mod := range modList {
		authorIDs = append(authorIDs, mod.Author)
	}
	authorNames := fetchSteamPersonaNames(authorIDs)
	for i := range modList {
		if name := authorNames[steamIDFromAuthor(modList[i].Author)]; name != "" {
			modList[i].Author = name
		}
	}

	return &SearchResult{
		Page:      page,
		Size:      size,
		Total:     total,
		TotalPage: int(math.Ceil(float64(total) / float64(size))),
		Data:      modList,
	}, nil
}

// SubscribeModByModId 订阅并下载模组
func (s *ModService) SubscribeModByModId(clusterName, modId, lang string) (*model.ModInfo, error) {
	if !isWorkshopId(modId) {
		// 非workshop mod，从本地读取
		return s.getLocalModInfo(clusterName, lang, modId)
	}

	// 从Steam API获取mod信息
	urlStr := "http://api.steampowered.com/IPublishedFileService/GetDetails/v1/"
	data := url.Values{}
	data.Set("key", steamAPIKey)
	data.Set("language", "6")
	data.Set("publishedfileids[0]", modId)
	urlStr = urlStr + "?" + data.Encode()

	req, err := http.NewRequest("GET", urlStr, nil)
	if err != nil {
		return nil, fmt.Errorf("创建请求失败: %w", err)
	}

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求Steam API失败: %w", err)
	}
	defer resp.Body.Close()

	var result map[string]interface{}
	err = json.NewDecoder(resp.Body).Decode(&result)
	if err != nil {
		return nil, fmt.Errorf("解析响应失败: %w", err)
	}

	dataList, ok := result["response"].(map[string]interface{})["publishedfiledetails"].([]interface{})
	if !ok || len(dataList) == 0 {
		return nil, errors.New("获取mod信息失败")
	}

	data2 := dataList[0].(map[string]interface{})
	img := data2["preview_url"].(string)
	auth := steamPersonaName(data2["creator"].(string))

	name := data2["title"].(string)
	lastTime := data2["time_updated"].(float64)
	description := data2["file_description"].(string)
	fileUrl := data2["file_url"]
	img = fmt.Sprintf("%s?imw=64&imh=64&ima=fit&impolicy=Letterbox&imcolor=%%23000000&letterbox=true", img)
	v := s.getVersion(data2["tags"])
	creatorAppid := data2["creator_appid"].(float64)
	consumerAppid := data2["consumer_appid"].(float64)

	var fileUrlStr = ""
	if fileUrl != nil {
		fileUrlStr = fileUrl.(string)
	}

	// 检查数据库中是否已存在
	existingMod, err := s.GetModByModId(modId)
	if err == nil && existingMod.Modid != "" {
		if lastTime == existingMod.LastTime {
			// 数据库记录没有变化，但必须确认本地仍然存在可用的 modinfo.lua 配置：
			// 缓存可能被清理，或旧版本曾把下载失败保存成空配置，不能继续当作成功返回。
			_, hasLocalModInfo := s.localModInfoPath(clusterName, modId)
			if hasLocalModInfo && !modConfigMissing(existingMod.ModConfig) {
				return existingMod, nil
			}
			modConfig, err := s.buildModConfig(clusterName, lang, modId, fileUrlStr)
			if err != nil {
				return nil, fmt.Errorf("读取或重新下载模组配置失败: %w", err)
			}
			existingMod.ModConfig = modConfig
			if err := s.db.Save(existingMod).Error; err != nil {
				return nil, fmt.Errorf("保存模组信息失败: %w", err)
			}
			return existingMod, nil
		}
		// 需要更新
		modConfig, err := s.buildModConfig(clusterName, lang, modId, fileUrlStr)
		if err != nil {
			return nil, err
		}

		existingMod.LastTime = lastTime
		existingMod.Name = name
		existingMod.Auth = auth
		existingMod.Description = description
		existingMod.Img = img
		existingMod.V = v
		existingMod.ModConfig = modConfig
		existingMod.Update = false
		if err := s.db.Save(existingMod).Error; err != nil {
			return nil, fmt.Errorf("保存模组信息失败: %w", err)
		}
		return existingMod, nil
	}

	// 新增mod
	modConfig, err := s.buildModConfig(clusterName, lang, modId, fileUrlStr)
	if err != nil {
		return nil, err
	}

	newModInfo := &model.ModInfo{
		Auth:          auth,
		ConsumerAppid: consumerAppid,
		CreatorAppid:  creatorAppid,
		Description:   description,
		FileUrl:       fileUrlStr,
		Modid:         modId,
		Img:           img,
		LastTime:      lastTime,
		Name:          name,
		V:             v,
		ModConfig:     modConfig,
	}

	if err := s.db.Create(newModInfo).Error; err != nil {
		return nil, fmt.Errorf("保存模组信息失败: %w", err)
	}
	return newModInfo, nil
}

// buildModConfig 读取本地或下载模组并序列化 mod_config，失败时返回具体原因。
func (s *ModService) buildModConfig(clusterName, lang, modId, fileUrlStr string) (string, error) {
	var metadata map[string]interface{}
	var err error
	if fileUrlStr != "" {
		metadata, err = s.getV1ModInfoConfig(clusterName, lang, modId, fileUrlStr)
	} else {
		metadata, err = s.getModInfoConfig(clusterName, lang, modId)
	}
	if err != nil {
		return "", err
	}
	modConfigJson, err := json.Marshal(metadata)
	if err != nil {
		return "", fmt.Errorf("解析模组配置失败: %w", err)
	}
	return string(modConfigJson), nil
}

// modConfigMissing 判断数据库中的 mod_config 是否为空；旧版本下载失败时会保存成空对象。
func modConfigMissing(modConfig string) bool {
	trimmed := strings.TrimSpace(modConfig)
	return trimmed == "" || trimmed == "{}" || trimmed == "null"
}

// GetMyModList 合并面板订阅记录和服务器已经安装的模组，不写入数据库或游戏文件。
func (s *ModService) GetMyModList(clusterName, lang string) ([]model.ModInfo, error) {
	var modInfos []model.ModInfo
	err := s.db.Find(&modInfos).Error
	if err != nil {
		return modInfos, err
	}
	modInfos, err = s.mergeInstalledMods(clusterName, lang, modInfos)
	if err != nil {
		return nil, err
	}
	authorIDs := make([]string, 0, len(modInfos))
	for _, modInfo := range modInfos {
		if steamID := steamIDFromAuthor(modInfo.Auth); steamID != "" {
			authorIDs = append(authorIDs, steamID)
		}
	}
	authorNames := fetchSteamPersonaNames(authorIDs)
	for i := range modInfos {
		if name := authorNames[steamIDFromAuthor(modInfos[i].Auth)]; name != "" {
			modInfos[i].Auth = name
		}
	}
	return modInfos, nil
}

// GetModByModId 根据modId获取模组
func (s *ModService) GetModByModId(modId string) (*model.ModInfo, error) {
	var modInfo model.ModInfo
	err := s.db.Where("modid = ?", modId).First(&modInfo).Error
	return &modInfo, err
}

// ClearModInfoCache 清除更新所需的元数据和下载缓存，保留世界正在使用的模组文件。
func (s *ModService) ClearModInfoCache(clusterName, modId string) error {
	config, err := s.dstConfig.GetDstConfig(clusterName)
	if err != nil {
		return err
	}
	if isWorkshopId(modId) && config.Mod_download_path != "" {
		path := filepath.Join(config.Mod_download_path, "steamapps", "workshop", "content", "322330", modId)
		if err := fileUtils.DeleteDir(path); err != nil {
			return err
		}
	}
	return s.db.Where("modid = ?", modId).Delete(&model.ModInfo{}).Error
}

// DeleteMod 取消订阅并清除未被世界使用的模组。
func (s *ModService) DeleteMod(clusterName, modId string) error {
	if modId == "" || strings.Trim(modId, "0123456789") != "" {
		return errors.New("无效的创意工坊模组 ID")
	}
	config, err := s.dstConfig.GetDstConfig(clusterName)
	if err != nil {
		return err
	}
	if clusterName == "" {
		clusterName = config.Cluster
	}
	usedIn, err := modUsedInAnyWorld(s.pathResolver.KleiBasePath(clusterName), modId)
	if err != nil {
		return fmt.Errorf("检查世界模组配置失败: %w", err)
	}
	if usedIn != "" {
		return fmt.Errorf("模组仍在 %s 的世界配置中，请先从该世界移除", usedIn)
	}

	if config.Force_install_dir != "" {
		if err := removeModSetupLine(filepath.Join(config.Force_install_dir, "mods", "dedicated_server_mods_setup.lua"), modId); err != nil {
			return fmt.Errorf("清理模组订阅配置失败: %w", err)
		}
	}
	paths := make(map[string]bool)
	if config.Mod_download_path != "" {
		paths[filepath.Join(config.Mod_download_path, "steamapps", "workshop", "content", "322330", modId)] = true
	}
	if config.Ugc_directory != "" {
		paths[filepath.Join(config.Ugc_directory, "content", "322330", modId)] = true
	}
	if config.Force_install_dir != "" {
		paths[filepath.Join(config.Force_install_dir, "mods", "workshop-"+modId)] = true
		installed, err := filepath.Glob(filepath.Join(config.Force_install_dir, "ugc_mods", "*", "*", "content", "322330", modId))
		if err != nil {
			return err
		}
		for _, path := range installed {
			paths[path] = true
		}
	}
	for path := range paths {
		if err := fileUtils.DeleteDir(path); err != nil {
			return fmt.Errorf("清理模组文件 %s 失败: %w", path, err)
		}
	}
	if err := s.db.Where("modid = ?", modId).Delete(&model.ModInfo{}).Error; err != nil {
		return fmt.Errorf("删除模组订阅记录失败: %w", err)
	}
	return nil
}

func modUsedInAnyWorld(kleiBasePath, modId string) (string, error) {
	clusters, err := os.ReadDir(kleiBasePath)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	key := regexp.MustCompile(`\[\s*["'](?:workshop-)?` + modId + `["']\s*\]`)
	for _, cluster := range clusters {
		if !cluster.IsDir() {
			continue
		}
		clusterPath := filepath.Join(kleiBasePath, cluster.Name())
		worlds, err := os.ReadDir(clusterPath)
		if err != nil {
			return "", err
		}
		for _, world := range worlds {
			if !world.IsDir() {
				continue
			}
			content, err := os.ReadFile(filepath.Join(clusterPath, world.Name(), "modoverrides.lua"))
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				return "", err
			}
			if key.Match(content) {
				return filepath.Join(cluster.Name(), world.Name()), nil
			}
		}
	}
	return "", nil
}

func removeModSetupLine(path, modId string) error {
	content, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	line := regexp.MustCompile(`^ServerModSetup\s*\(\s*["']` + modId + `["']\s*\)(?:\s*--.*)?$`)
	lines := strings.SplitAfter(string(content), "\n")
	remaining := make([]string, 0, len(lines))
	removed := false
	for _, item := range lines {
		if line.MatchString(strings.TrimSpace(item)) {
			removed = true
			continue
		}
		remaining = append(remaining, item)
	}
	if !removed {
		return nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	return os.WriteFile(path, []byte(strings.Join(remaining, "")), info.Mode().Perm())
}

// UpdateAllModInfos 批量更新所有模组信息
func (s *ModService) UpdateAllModInfos(clusterName, lang string) error {
	var modInfos []model.ModInfo
	var needUpdateList []model.ModInfo
	var workshopIds []string

	s.db.Find(&modInfos)

	for i := range modInfos {
		workshopIds = append(workshopIds, modInfos[i].Modid)
	}

	publishedFileDetails, err := s.getPublishedFileDetailsBatched(workshopIds, 20)
	if err != nil {
		return err
	}

	for i := range publishedFileDetails {
		publishedfiledetail := publishedFileDetails[i]
		for j := range modInfos {
			if modInfos[j].Modid == publishedfiledetail.Publishedfileid && modInfos[j].LastTime < publishedfiledetail.TimeUpdated {
				needUpdateList = append(needUpdateList, modInfos[i])
			}
		}
	}

	var wg sync.WaitGroup
	wg.Add(len(needUpdateList))

	for i := range needUpdateList {
		go func(i int) {
			defer func() {
				if r := recover(); r != nil {
					log.Println(r)
				}
				wg.Done()
			}()
			modId := needUpdateList[i].Modid
			// 删除之前的数据
			config, _ := s.dstConfig.GetDstConfig(clusterName)
			modDownloadPath := config.Mod_download_path
			modPath := filepath.Join(modDownloadPath, "/steamapps/workshop/content/322330/", modId)
			_ = fileUtils.DeleteDir(modPath)
			_, _ = s.SubscribeModByModId(clusterName, modId, lang)
		}(i)
	}
	wg.Wait()

	return nil
}

// DeleteSetupWorkshop 删除所有workshop模组
func (s *ModService) DeleteSetupWorkshop(clusterName string) error {
	config, err := s.dstConfig.GetDstConfig(clusterName)
	if err != nil {
		return err
	}

	dstPath := config.Force_install_dir
	modsPath := filepath.Join(dstPath, "mods")

	directories, err := fileUtils.ListDirectories(modsPath)
	if err != nil {
		return fmt.Errorf("列出目录失败: %w", err)
	}

	var workshopList []string
	for _, directory := range directories {
		if strings.Contains(directory, "workshop") {
			workshopList = append(workshopList, directory)
		}
	}

	for _, workshop := range workshopList {
		err := fileUtils.DeleteDir(workshop)
		if err != nil {
			return err
		}
	}

	return nil
}

// SaveModInfo 保存模组信息
func (s *ModService) SaveModInfo(modInfo *model.ModInfo) error {
	return s.db.Save(modInfo).Error
}

// AddModInfo 手动添加模组
func (s *ModService) AddModInfo(clusterName, lang, modid, modinfo, modDownloadPath string) error {
	// 创建workshop文件
	workshopDirPath := filepath.Join(modDownloadPath, "/steamapps/workshop/content/322330", modid)
	fileUtils.CreateDirIfNotExists(workshopDirPath)

	modinfoPath := filepath.Join(workshopDirPath, "modinfo.lua")
	err := fileUtils.CreateFileIfNotExists(modinfoPath)
	if err != nil {
		return fmt.Errorf("创建modinfo.lua失败: %w", err)
	}

	err = fileUtils.WriterTXT(modinfoPath, modinfo)
	if err != nil {
		return fmt.Errorf("写入modinfo.lua失败: %w", err)
	}

	// 添加到数据库
	return s.addModInfoToDb(clusterName, lang, modid)
}

// GetUgcModInfo 获取UGC模组信息
func (s *ModService) GetUgcModInfo(clusterName, levelName string) ([]WorkshopItemDetail, error) {
	acfPath := s.pathResolver.GetUgcAcfPath(clusterName, levelName)
	installed := s.parseACFFile(acfPath)
	details := make([]WorkshopItemDetail, 0, len(installed))
	modIDs := make([]string, 0, len(installed))
	indexes := make(map[string]int, len(installed))
	for id, item := range installed {
		indexes[id] = len(details)
		modIDs = append(modIDs, id)
		details = append(details, WorkshopItemDetail{
			WorkShopId: id, Name: id, Timeupdated: item.TimeUpdated,
		})
	}
	if len(modIDs) == 0 {
		return details, nil
	}
	// Installed files remain visible when Steam metadata is temporarily unavailable.
	published, err := s.getPublishedFileDetailsBatched(modIDs, 20)
	if err != nil {
		return details, nil
	}
	for _, item := range published {
		if index, ok := indexes[item.Publishedfileid]; ok {
			details[index].Name = item.Title
			details[index].Img = item.PreviewURL
			details[index].Timelast = item.TimeUpdated
		}
	}
	return details, nil
}

// DeleteUgcModFile 删除指定分片已下载的 UGC 模组文件，并清理 ACF 中的下载记录。
// 只删除文件/记录，不取消订阅、不修改世界配置；模组仍在启用列表时，下次启动世界会被重新下载。
func (s *ModService) DeleteUgcModFile(clusterName, levelName, workshopId string) error {
	if !isWorkshopId(workshopId) {
		return errors.New("无效的创意工坊模组 ID")
	}
	modFilePath := s.pathResolver.GetUgcWorkshopModPath(clusterName, levelName, workshopId)
	if fileUtils.Exists(modFilePath) {
		if err := fileUtils.DeleteDir(modFilePath); err != nil {
			return err
		}
	}
	// 同步清理 appworkshop_322330.acf，否则刷新后列表仍会从 ACF 记录里读回该模组。
	acfPath := s.pathResolver.GetUgcAcfPath(clusterName, levelName)
	if _, err := removeACFWorkshopItem(acfPath, workshopId); err != nil {
		return fmt.Errorf("清理 UGC 下载记录失败: %w", err)
	}
	return nil
}

// ===== 私有方法 =====

// parseACFFile 解析 ACF 文件中的已安装工坊条目（只读 WorkshopItemsInstalled 段）。
func (s *ModService) parseACFFile(filePath string) map[string]WorkshopItem {
	return parseWorkshopItemsInstalled(filePath)
}

// getModInfoConfig 获取mod配置信息；本地与缓存都没有 modinfo.lua 时下载，失败返回错误。
func (s *ModService) getModInfoConfig(clusterName, lang, modId string) (map[string]interface{}, error) {
	// 1. 游戏安装目录或面板下载缓存里已有 modinfo.lua，直接读取，不再重复下载。
	if modinfoPath, ok := s.localModInfoPath(clusterName, modId); ok {
		return s.readModInfoStrict(lang, modId, modinfoPath)
	}

	// 2. 同一 modId 的下载串行化，避免并发请求重复下载。
	unlock := s.lockModDownload(modId)
	defer unlock()
	// 获取锁后再次检查：等待期间其它请求可能已经下载完成。
	if modinfoPath, ok := s.localModInfoPath(clusterName, modId); ok {
		return s.readModInfoStrict(lang, modId, modinfoPath)
	}

	config, err := s.dstConfig.GetDstConfig(clusterName)
	if err != nil {
		return nil, fmt.Errorf("获取DST配置失败: %w", err)
	}
	modDownloadPath := strings.TrimSpace(config.Mod_download_path)
	if modDownloadPath == "" {
		return nil, errors.New("未配置模组下载目录 mod_download_path")
	}
	if !isWorkshopId(modId) {
		return nil, fmt.Errorf("未找到本地模组 modinfo.lua: %s", modId)
	}
	if strings.TrimSpace(config.Steamcmd) == "" {
		return nil, errors.New("未配置 SteamCMD 路径，无法下载模组")
	}

	modPath := filepath.Join(modDownloadPath, "steamapps", "workshop", "content", "322330", modId)
	if err := s.downloadModWithSteamCMD(config, modDownloadPath, modId); err != nil {
		return nil, err
	}

	// 3. 下载后必须校验 modinfo.lua 确实存在，不能仅凭命令输出判断成功。
	modinfoPath := filepath.Join(modPath, "modinfo.lua")
	if !fileUtils.Exists(modinfoPath) {
		return nil, fmt.Errorf("SteamCMD 未下载到 modinfo.lua: %s（请确认模组ID是否正确以及 SteamCMD 配置）", modinfoPath)
	}
	return s.readModInfoStrict(lang, modId, modinfoPath)
}

// localModInfoPath 返回本地可用的 modinfo.lua 路径：优先游戏已安装目录，其次面板下载缓存。
func (s *ModService) localModInfoPath(clusterName, modId string) (string, bool) {
	config, err := s.dstConfig.GetDstConfig(clusterName)
	if err != nil {
		return "", false
	}
	var paths []string
	if dstModInstalledPath, ok := s.getDstUcgsModsInstalledPath(clusterName, modId); ok {
		paths = append(paths, filepath.Join(dstModInstalledPath, "modinfo.lua"))
	}
	if config.Force_install_dir != "" {
		paths = append(paths,
			filepath.Join(config.Force_install_dir, "mods", "workshop-"+modId, "modinfo.lua"),
			filepath.Join(config.Force_install_dir, "mods", modId, "modinfo.lua"),
		)
	}
	if config.Mod_download_path != "" {
		paths = append(paths, filepath.Join(config.Mod_download_path, "steamapps", "workshop", "content", "322330", modId, "modinfo.lua"))
	}
	for _, path := range paths {
		if fileUtils.Exists(path) {
			return path, true
		}
	}
	return "", false
}

// lockModDownload 获取指定 modId 的下载锁，返回解锁函数。
func (s *ModService) lockModDownload(modId string) func() {
	value, _ := s.modDownloadLocks.LoadOrStore(modId, &sync.Mutex{})
	mutex := value.(*sync.Mutex)
	mutex.Lock()
	return mutex.Unlock
}

// downloadModWithSteamCMD 调用 SteamCMD 下载模组，失败或超时时返回带输出尾部的错误。
func (s *ModService) downloadModWithSteamCMD(config dstConfig.DstConfig, modDownloadPath, modId string) error {
	if err := os.MkdirAll(modDownloadPath, os.ModePerm); err != nil {
		return fmt.Errorf("创建模组下载目录失败: %w", err)
	}

	if runtime.GOOS == "windows" {
		steamcmdPath := strings.TrimSpace(config.Steamcmd)
		steamcmdExe := filepath.Join(steamcmdPath, "steamcmd.exe")
		if !fileUtils.Exists(steamcmdExe) {
			return fmt.Errorf("SteamCMD 路径无效，未找到 %s", steamcmdExe)
		}
		cmd := fmt.Sprintf("cd /d \"%s\" && steamcmd.exe +login anonymous +force_install_dir \"%s\" +workshop_download_item 322330 %s +quit", steamcmdPath, modDownloadPath, modId)
		log.Println("正在下载模组 command:", cmd)
		output, err := shellUtils.ExecuteCommandInWin(cmd)
		if err != nil {
			return fmt.Errorf("SteamCMD 下载模组 %s 失败: %v，输出: %s", modId, err, tailText(output, steamCmdOutputTailLimit))
		}
		return nil
	}

	steamcmdPath := strings.TrimSpace(config.Steamcmd)
	steamcmdFile := filepath.Join(steamcmdPath, "steamcmd")
	if !fileUtils.Exists(steamcmdFile) {
		steamcmdFile = filepath.Join(steamcmdPath, "steamcmd.sh")
	}
	if !fileUtils.Exists(steamcmdFile) {
		return fmt.Errorf("SteamCMD 路径无效，未找到 steamcmd 或 steamcmd.sh: %s", steamcmdPath)
	}

	ctx, cancel := context.WithTimeout(context.Background(), modDownloadTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, steamcmdFile, "+login", "anonymous", "+force_install_dir", modDownloadPath, "+workshop_download_item", "322330", modId, "+quit")
	log.Println("正在下载模组 command:", cmd)
	output, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return fmt.Errorf("SteamCMD 下载模组 %s 超时（限制 %s），输出: %s", modId, modDownloadTimeout, tailText(string(output), steamCmdOutputTailLimit))
	}
	if err != nil {
		return fmt.Errorf("SteamCMD 下载模组 %s 失败: %v，输出: %s", modId, err, tailText(string(output), steamCmdOutputTailLimit))
	}
	return nil
}

// tailText 返回文本末尾最多 limit 个字符，用于错误信息中保留关键输出。
func tailText(text string, limit int) string {
	text = strings.TrimSpace(text)
	if limit <= 0 || len(text) <= limit {
		return text
	}
	return "..." + text[len(text)-limit:]
}

// getV1ModInfoConfig 从v1 mod中获取配置，下载或解压失败时返回错误。
func (s *ModService) getV1ModInfoConfig(clusterName, lang, modid, fileUrl string) (map[string]interface{}, error) {
	log.Println("开始下载 v1 mod，并提取 modinfo.lua 文件")
	modinfo := map[string][]byte{"modinfo": nil, "modinfo_chs": nil}
	var tmp bytes.Buffer

	for i := 0; i < 3; i++ {
		tmp.Reset()
		req, err := http.NewRequest("GET", fileUrl, nil)
		if err != nil {
			log.Println(fileUrl, "下载失败", err)
			continue
		}
		client := http.Client{
			Timeout: time.Duration(10 * time.Second),
		}
		res, err := client.Do(req)
		if err != nil {
			log.Println(fileUrl, "下载失败", err)
			continue
		}
		_, err = tmp.ReadFrom(res.Body)
		res.Body.Close()
		if err != nil {
			log.Println(err)
			continue
		}
		break
	}

	if tmp.Len() == 0 {
		return nil, fmt.Errorf("模组压缩包下载失败（已重试 3 次）: %s", fileUrl)
	}

	log.Println(fileUrl, "下载成功，开始解压")
	zipReader, err := zip.NewReader(bytes.NewReader(tmp.Bytes()), int64(tmp.Len()))
	if err != nil {
		return nil, fmt.Errorf("模组压缩包解析失败: %w", err)
	}

	if err := s.unzipToDir(zipReader, filepath.Join(s.pathResolver.GetUgcModPath(clusterName), "content", "322330", modid)); err != nil {
		return nil, fmt.Errorf("解压模组文件失败: %w", err)
	}

	for _, file := range zipReader.File {
		switch file.Name {
		case "modinfo.lua":
			f, err := file.Open()
			if err != nil {
				return nil, fmt.Errorf("读取模组压缩包中的 modinfo.lua 失败: %w", err)
			}
			modinfoBytes, err := ioutil.ReadAll(f)
			f.Close()
			if err != nil {
				return nil, fmt.Errorf("读取模组压缩包中的 modinfo.lua 失败: %w", err)
			}
			modinfo["modinfo"] = modinfoBytes
		case "modinfo_chs.lua":
			f, err := file.Open()
			if err != nil {
				continue
			}
			modinfoBytes, err := ioutil.ReadAll(f)
			f.Close()
			if err == nil {
				modinfo["modinfo_chs"] = modinfoBytes
			}
		}
	}

	if len(modinfo["modinfo"]) == 0 {
		return nil, fmt.Errorf("模组压缩包中缺少 modinfo.lua: %s", fileUrl)
	}
	return s.parseModInfoLua(lang, modid, string(modinfo["modinfo"])), nil
}

// getDstUcgsModsInstalledPath 获取饥荒本身modid的位置
func (s *ModService) getDstUcgsModsInstalledPath(clusterName, modid string) (string, bool) {
	config, _ := s.dstConfig.GetDstConfig(clusterName)
	var masterModFilePath, caveModFilePath string

	if config.Ugc_directory != "" {
		masterModFilePath = filepath.Join(s.pathResolver.GetUgcModPath(clusterName), "content", "322330", modid)
		caveModFilePath = filepath.Join(s.pathResolver.GetUgcModPath(clusterName), "content", "322330", modid)
	} else {
		masterModFilePath = filepath.Join(config.Force_install_dir, "ugc_mods", clusterName, "Master", "content", "322330", modid)
		caveModFilePath = filepath.Join(config.Force_install_dir, "ugc_mods", clusterName, "Caves", "content", "322330", modid)
	}

	if fileUtils.Exists(masterModFilePath) {
		return masterModFilePath, true
	}
	if fileUtils.Exists(caveModFilePath) {
		return caveModFilePath, true
	}
	return "", false
}

// readModInfo 读取modinfo.lua文件
func (s *ModService) readModInfo(lang, modId, modinfoPath string) map[string]interface{} {
	metadata, err := s.readModInfoStrict(lang, modId, modinfoPath)
	if err != nil {
		log.Println("Error reading modinfo.lua:", err)
		return make(map[string]interface{})
	}
	return metadata
}

// readModInfoStrict 读取modinfo.lua文件，失败时返回错误。
func (s *ModService) readModInfoStrict(lang, modId, modinfoPath string) (map[string]interface{}, error) {
	script, err := ioutil.ReadFile(modinfoPath)
	if err != nil {
		return nil, fmt.Errorf("读取 modinfo.lua 失败: %w", err)
	}
	return s.parseModInfoLua(lang, modId, string(script)), nil
}

// parseModInfoLua 解析modinfo.lua文件
func (s *ModService) parseModInfoLua(lang, modId, script string) map[string]interface{} {
	L := lua.NewState()
	defer L.Close()

	L.SetGlobal("locale", lua.LString(lang))
	L.SetGlobal("folder_name", lua.LString(fmt.Sprintf("workshop-%s", modId)))
	L.SetGlobal("ChooseTranslationTable", L.NewFunction(func(L *lua.LState) int {
		tbl := L.ToTable(1)
		langTbl := tbl.RawGetString(lang)
		if langTbl != lua.LNil {
			L.Push(langTbl)
		} else {
			L.Push(tbl.RawGetInt(1))
		}
		return 1
	}))

	L.DoString(script)

	global := L.Get(lua.GlobalsIndex).(*lua.LTable)
	m := make(map[string]interface{})
	global.ForEach(func(k lua.LValue, v lua.LValue) {
		if !excludeList[k.String()] && v.Type() != lua.LTFunction {
			m[k.String()] = toInterface(v)
		}
	})

	return m
}

// getVersion 从tags中获取版本号
func (s *ModService) getVersion(tags interface{}) string {
	tagList, ok := tags.([]interface{})
	if !ok {
		return ""
	}
	for _, tag := range tagList {
		tagMap, ok := tag.(map[string]interface{})
		if !ok {
			continue
		}
		tagStr, ok := tagMap["tag"].(string)
		if !ok {
			continue
		}
		if len(tagStr) > 8 && tagStr[:8] == "version:" {
			return tagStr[8:]
		}
	}
	return ""
}

// searchModInfoByWorkshopId 通过workshopId搜索mod信息
func (s *ModService) searchModInfoByWorkshopId(modID int) ModInfo {
	urlStr := "http://api.steampowered.com/IPublishedFileService/GetDetails/v1/"
	data := url.Values{}
	data.Set("key", steamAPIKey)
	data.Set("language", "6")
	data.Set("publishedfileids[0]", strconv.Itoa(modID))
	urlStr = urlStr + "?" + data.Encode()

	req, err := http.NewRequest("GET", urlStr, nil)
	if err != nil {
		return ModInfo{}
	}

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return ModInfo{}
	}
	defer resp.Body.Close()

	var result map[string]interface{}
	err = json.NewDecoder(resp.Body).Decode(&result)
	if err != nil {
		return ModInfo{}
	}

	dataList, ok := result["response"].(map[string]interface{})["publishedfiledetails"].([]interface{})
	if !ok || len(dataList) == 0 {
		return ModInfo{}
	}

	data2 := dataList[0].(map[string]interface{})
	if data2["consumer_appid"] == nil || data2["consumer_appid"].(float64) != 322330 {
		return ModInfo{}
	}

	img := data2["preview_url"].(string)
	auth := steamPersonaName(data2["creator"].(string))

	modId := data2["publishedfileid"].(string)
	name := data2["title"].(string)
	description := data2["file_description"].(string)
	img = fmt.Sprintf("%s?imw=64&imh=64&ima=fit&impolicy=Letterbox&imcolor=%%23000000&letterbox=true", img)

	return ModInfo{
		ID:     modId,
		Name:   name,
		Author: auth,
		Desc:   description,
		Time:   int(data2["time_updated"].(float64)),
		Sub:    int(data2["subscriptions"].(float64)),
		Img:    img,
	}
}

// getLocalModInfo 获取本地mod信息
func (s *ModService) getLocalModInfo(clusterName, lang, modId string) (*model.ModInfo, error) {
	metadata, err := s.getModInfoConfig(clusterName, lang, modId)
	if err != nil {
		return nil, err
	}
	modConfigJson, err := json.Marshal(metadata)
	if err != nil {
		return nil, fmt.Errorf("解析模组配置失败: %w", err)
	}
	modConfig := string(modConfigJson)

	newModInfo := &model.ModInfo{
		Auth:          "",
		ConsumerAppid: 0,
		CreatorAppid:  0,
		Description:   "",
		Modid:         modId,
		Img:           "xxx",
		LastTime:      0,
		Name:          modId,
		V:             "",
		ModConfig:     modConfig,
	}

	if err := s.db.Create(newModInfo).Error; err != nil {
		return nil, fmt.Errorf("保存模组信息失败: %w", err)
	}
	return newModInfo, nil
}

// addModInfoToDb 添加mod信息到数据库
func (s *ModService) addModInfoToDb(clusterName, lang, modid string) error {
	var modInfo *model.ModInfo
	var err error

	if !isWorkshopId(modid) {
		modInfo, err = s.getLocalModInfo(clusterName, lang, modid)
	} else {
		// 从Steam获取mod基本信息
		modInfo, err = s.getModInfo2(modid)
	}

	if err != nil {
		return fmt.Errorf("获取modinfo失败: %w", err)
	}

	metadata, err := s.getModInfoConfig(clusterName, lang, modid)
	if err != nil {
		return fmt.Errorf("获取modinfo配置失败: %w", err)
	}
	modConfigJson, err := json.Marshal(metadata)
	if err != nil {
		return fmt.Errorf("解析模组配置失败: %w", err)
	}
	modConfig := string(modConfigJson)

	// 从数据库查找是否已存在
	oldModinfo, err := s.GetModByModId(modid)

	if err == nil && oldModinfo.Modid != "" {
		// 更新
		oldModinfo.LastTime = modInfo.LastTime
		oldModinfo.Name = modInfo.Name
		oldModinfo.Auth = modInfo.Auth
		oldModinfo.Description = modInfo.Description
		oldModinfo.Img = modInfo.Img
		oldModinfo.V = modInfo.V
		oldModinfo.ModConfig = modConfig
		if err := s.db.Save(oldModinfo).Error; err != nil {
			return fmt.Errorf("保存模组信息失败: %w", err)
		}
		return nil
	}

	// 新增
	modInfo.ModConfig = modConfig
	if err := s.db.Create(modInfo).Error; err != nil {
		return fmt.Errorf("保存模组信息失败: %w", err)
	}
	return nil
}

// getModInfo2 从Steam API获取mod基本信息
func (s *ModService) getModInfo2(modID string) (*model.ModInfo, error) {
	urlStr := "http://api.steampowered.com/IPublishedFileService/GetDetails/v1/"
	data := url.Values{}
	data.Set("key", steamAPIKey)
	data.Set("language", "6")
	data.Set("publishedfileids[0]", modID)
	urlStr = urlStr + "?" + data.Encode()

	req, err := http.NewRequest("GET", urlStr, nil)
	if err != nil {
		return nil, err
	}

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var result map[string]interface{}
	err = json.NewDecoder(resp.Body).Decode(&result)
	if err != nil {
		return nil, err
	}

	dataList, ok := result["response"].(map[string]interface{})["publishedfiledetails"].([]interface{})
	if !ok || len(dataList) == 0 {
		return nil, errors.New("获取mod信息失败")
	}

	data2 := dataList[0].(map[string]interface{})
	img := data2["preview_url"].(string)
	auth := steamPersonaName(data2["creator"].(string))

	modId := data2["publishedfileid"].(string)
	name := data2["title"].(string)
	lastTime := data2["time_updated"].(float64)
	description := data2["file_description"].(string)
	fileUrl := data2["file_url"]
	img = fmt.Sprintf("%s?imw=64&imh=64&ima=fit&impolicy=Letterbox&imcolor=%%23000000&letterbox=true", img)
	v := s.getVersion(data2["tags"])
	creatorAppid := data2["creator_appid"].(float64)
	consumerAppid := data2["consumer_appid"].(float64)

	var fileUrlStr = ""
	if fileUrl != nil {
		fileUrlStr = fileUrl.(string)
	}

	return &model.ModInfo{
		Auth:          auth,
		ConsumerAppid: consumerAppid,
		CreatorAppid:  creatorAppid,
		Description:   description,
		FileUrl:       fileUrlStr,
		Modid:         modId,
		Img:           img,
		LastTime:      lastTime,
		Name:          name,
		V:             v,
	}, nil
}

// getPublishedFileDetailsBatched 批量获取mod详情
func (s *ModService) getPublishedFileDetailsBatched(workshopIds []string, batchSize int) ([]Publishedfiledetail, error) {
	var allPublishedFileDetails []Publishedfiledetail

	for i := 0; i < len(workshopIds); i += batchSize {
		end := i + batchSize
		if end > len(workshopIds) {
			end = len(workshopIds)
		}

		batch := workshopIds[i:end]
		publishedFileDetails, err := s.getPublishedFileDetailsWithGet(batch)
		if err != nil {
			return nil, err
		}

		allPublishedFileDetails = append(allPublishedFileDetails, publishedFileDetails...)
	}

	return allPublishedFileDetails, nil
}

// getPublishedFileDetailsWithGet 通过GET方式获取mod详情
func (s *ModService) getPublishedFileDetailsWithGet(workshopIds []string) ([]Publishedfiledetail, error) {
	urlStr := "https://api.steampowered.com/IPublishedFileService/GetDetails/v1/"
	data := url.Values{}
	data.Set("key", steamAPIKey)
	data.Set("language", "6")
	for i := range workshopIds {
		data.Set("publishedfileids["+strconv.Itoa(i)+"]", workshopIds[i])
	}
	urlStr = urlStr + "?" + data.Encode()

	req, err := http.NewRequest("GET", urlStr, nil)
	if err != nil {
		return nil, err
	}

	client := &http.Client{Timeout: 10 * time.Second}
	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()

	var publishedFileDetailsData struct {
		Response struct {
			Publishedfiledetails []Publishedfiledetail `json:"publishedfiledetails"`
		} `json:"response"`
	}

	err = json.NewDecoder(res.Body).Decode(&publishedFileDetailsData)
	if err != nil {
		return nil, err
	}

	return publishedFileDetailsData.Response.Publishedfiledetails, nil
}

// getPublishedFileDetails 通过POST方式获取mod详情
func (s *ModService) getPublishedFileDetails(workshopIds []string) ([]Publishedfiledetail, error) {
	url := "https://api.steampowered.com/ISteamRemoteStorage/GetPublishedFileDetails/v1/"
	payload := &bytes.Buffer{}
	writer := multipart.NewWriter(payload)

	_ = writer.WriteField("itemcount", strconv.Itoa(len(workshopIds)))
	for i := range workshopIds {
		_ = writer.WriteField("publishedfileids["+strconv.Itoa(i)+"]", workshopIds[i])
	}

	err := writer.Close()
	if err != nil {
		return nil, err
	}

	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}
	client := &http.Client{Transport: tr}
	req, err := http.NewRequest("POST", url, payload)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", writer.FormDataContentType())
	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()

	var publishedFileDetailsData struct {
		Response struct {
			Result               int                   `json:"result"`
			Resultcount          int                   `json:"resultcount"`
			Publishedfiledetails []Publishedfiledetail `json:"publishedfiledetails"`
		} `json:"response"`
	}

	err = json.NewDecoder(res.Body).Decode(&publishedFileDetailsData)
	if err != nil {
		return nil, err
	}

	if publishedFileDetailsData.Response.Result == 1 {
		return publishedFileDetailsData.Response.Publishedfiledetails, nil
	}

	return nil, errors.New("请求失败")
}

// unzipToDir 解压zip文件到指定目录
func (s *ModService) unzipToDir(zipReader *zip.Reader, destDir string) error {
	for _, file := range zipReader.File {
		destPath := filepath.Join(destDir, file.Name)

		if !filepath.HasPrefix(destPath, filepath.Clean(destDir)+string(os.PathSeparator)) {
			return fmt.Errorf("非法文件路径: %s", destPath)
		}

		if file.FileInfo().IsDir() {
			err := os.MkdirAll(destPath, os.ModePerm)
			if err != nil {
				return err
			}
			continue
		}

		if err := os.MkdirAll(filepath.Dir(destPath), os.ModePerm); err != nil {
			return err
		}

		outFile, err := os.OpenFile(destPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, file.Mode())
		if err != nil {
			return err
		}
		defer outFile.Close()

		rc, err := file.Open()
		if err != nil {
			return err
		}
		defer rc.Close()

		_, err = io.Copy(outFile, rc)
		if err != nil {
			return err
		}
	}
	return nil
}

// ===== 辅助函数 =====

// excludeList Lua自带对象名称
var excludeList = map[string]bool{
	"_G": true, "assert": true, "collectgarbage": true, "dofile": true, "error": true,
	"getmetatable": true, "ipairs": true, "load": true, "loadfile": true, "module": true,
	"next": true, "pairs": true, "pcall": true, "print": true, "rawequal": true, "rawget": true,
	"rawset": true, "require": true, "select": true, "setmetatable": true, "tonumber": true,
	"tostring": true, "type": true, "unpack": true, "xpcall": true, "debug": true, "_VERSION": true,
	"os": true, "_GOPHER_LUA_VERSION": true, "string": true, "math": true, "io": true, "channel": true,
	"package": true, "coroutine": true, "table": true,
}

// toInterface 将Lua值转换为interface{}
func toInterface(lv lua.LValue) interface{} {
	switch lv.Type() {
	case lua.LTNil:
		return nil
	case lua.LTBool:
		return bool(lv.(lua.LBool))
	case lua.LTNumber:
		return float64(lv.(lua.LNumber))
	case lua.LTString:
		return lv.String()
	case lua.LTTable:
		t := lv.(*lua.LTable)
		if isTableArray(t) {
			arr := make([]interface{}, t.Len())
			t.ForEach(func(i lua.LValue, v lua.LValue) {
				index := int(float64(i.(lua.LNumber)) - 1)
				if index != -1 && index < len(arr) {
					arr[index] = toInterface(v)
				}
			})
			return arr
		}
		return toMap(t)
	default:
		return lv.String()
	}
}

// toMap 将Lua table转换为map
func toMap(t *lua.LTable) map[string]interface{} {
	m := make(map[string]interface{})
	t.ForEach(func(k lua.LValue, v lua.LValue) {
		key := ""
		switch k.Type() {
		case lua.LTString:
			key = k.String()
		case lua.LTNumber:
			key = fmt.Sprintf("%g", float64(k.(lua.LNumber)))
		default:
			key = fmt.Sprintf("%v", k)
		}
		m[key] = toInterface(v)
	})
	return m
}

// isTableArray 判断Lua table是否为数组
func isTableArray(t *lua.LTable) bool {
	maxIndex := 0
	isSequential := true
	t.ForEach(func(k lua.LValue, v lua.LValue) {
		if i, ok := k.(lua.LNumber); ok {
			if i != lua.LNumber(int(i)) {
				isSequential = false
			} else if int(i) > maxIndex {
				maxIndex = int(i)
			}
		} else {
			isSequential = false
		}
	})
	return isSequential && maxIndex == t.Len()
}

// isWorkshopId 判断是否为workshop ID
func isWorkshopId(id string) bool {
	_, err := strconv.Atoi(id)
	return err == nil
}

// isModId 判断字符串是否为modID
func isModId(str string) (int, bool) {
	id, err := strconv.Atoi(str)
	return id, err == nil
}
