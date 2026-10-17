package mod

import (
	"dst-admin-go/internal/model"
	"encoding/json"
	"path/filepath"
	"strings"
)

// Read installed modinfo.lua files directly, including servers created before the panel.
// Do not call subscription/download helpers here: listing mods must remain read-only.
func (s *ModService) mergeInstalledMods(clusterName, lang string, mods []model.ModInfo) ([]model.ModInfo, error) {
	config, err := s.dstConfig.GetDstConfig(clusterName)
	if err != nil {
		return nil, err
	}
	if clusterName == "" {
		clusterName = config.Cluster
	}
	var patterns []string
	if config.Ugc_directory != "" {
		patterns = append(patterns, filepath.Join(s.pathResolver.GetUgcModPath(clusterName), "content", "322330", "*", "modinfo.lua"))
	} else if config.Force_install_dir != "" && clusterName != "" {
		patterns = append(patterns, filepath.Join(s.pathResolver.GetUgcModPath(clusterName), clusterName, "*", "content", "322330", "*", "modinfo.lua"))
	}
	if config.Force_install_dir != "" {
		patterns = append(patterns, filepath.Join(config.Force_install_dir, "mods", "workshop-*", "modinfo.lua"))
	}
	indexes := make(map[string]int, len(mods))
	for i := range mods {
		mods[i].Installed = false
		mods[i].Downloaded = false
		indexes[mods[i].Modid] = i
	}
	seen := make(map[string]bool)
	for _, pattern := range patterns {
		files, err := filepath.Glob(pattern)
		if err != nil {
			return nil, err
		}
		for _, path := range files {
			id := strings.TrimPrefix(filepath.Base(filepath.Dir(path)), "workshop-")
			if !isWorkshopId(id) || seen[id] {
				continue
			}
			seen[id] = true
			index, exists := indexes[id]
			if !exists {
				index = len(mods)
				indexes[id] = index
				mods = append(mods, model.ModInfo{Modid: id})
			}
			item := &mods[index]
			// 游戏目录里已有 modinfo.lua，说明模组文件已经安装到游戏侧，自然也已下载。
			item.Installed = true
			item.Downloaded = true
			metadata := s.readModInfo(lang, id, path)
			if name, ok := metadata["name"].(string); ok && (item.Name == "" || item.Name == id) {
				item.Name = name
			}
			if item.Name == "" {
				item.Name = id
			}
			if author, ok := metadata["author"].(string); ok && item.Auth == "" {
				item.Auth = author
			}
			if description, ok := metadata["description"].(string); ok && item.Description == "" {
				item.Description = description
			}
			if version, ok := metadata["version"].(string); ok {
				item.V = version
			}
			if metadata["name"] != nil {
				if data, err := json.Marshal(metadata); err == nil {
					item.ModConfig = string(data)
				}
			}
		}
	}

	// 面板下载缓存里有 modinfo.lua 的模组同样视为已下载；这里只标记列表中已有的条目
	//（数据库订阅记录或已安装项），不使用缓存文件新增条目，列表保持只读。
	if config.Mod_download_path != "" {
		cachePattern := filepath.Join(config.Mod_download_path, "steamapps", "workshop", "content", "322330", "*", "modinfo.lua")
		files, err := filepath.Glob(cachePattern)
		if err != nil {
			return nil, err
		}
		for _, path := range files {
			id := filepath.Base(filepath.Dir(path))
			if !isWorkshopId(id) {
				continue
			}
			if index, exists := indexes[id]; exists {
				mods[index].Downloaded = true
			}
		}
	}
	return mods, nil
}
