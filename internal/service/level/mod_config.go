package level

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"dst-admin-go/internal/service/levelConfig"
)

type modWrite struct {
	real      string
	writePath string
	content   string
	worlds    []string
}

func (l *LevelService) planModWrites(clusterName string, levels []levelConfig.LevelInfo) ([]modWrite, error) {
	clusterPath := l.resolver.ClusterPath(clusterName)
	grouped := map[string]*modWrite{}
	var order []string
	for _, level := range levels {
		writePath, real, err := resolveModTarget(clusterPath, level.Uuid)
		if err != nil {
			return nil, err
		}
		name := level.LevelName
		if name == "" {
			name = level.Uuid
		}
		existing, ok := grouped[real]
		if !ok {
			grouped[real] = &modWrite{
				real:      real,
				writePath: writePath,
				content:   level.Modoverrides,
				worlds:    []string{name},
			}
			order = append(order, real)
			continue
		}
		existing.worlds = append(existing.worlds, name)
		if existing.content != level.Modoverrides {
			return nil, fmt.Errorf("%s 的模组配置指向同一文件但内容不同", strings.Join(existing.worlds, "、"))
		}
	}
	writes := make([]modWrite, 0, len(order))
	for _, real := range order {
		writes = append(writes, *grouped[real])
	}
	return writes, nil
}

func (l *LevelService) attachModConfigScope(clusterName string, levels []levelConfig.LevelInfo) []levelConfig.LevelInfo {
	clusterPath := l.resolver.ClusterPath(clusterName)
	type resolved struct {
		real string
		err  string
	}
	items := make([]resolved, len(levels))
	groups := map[string][]string{}
	for i, level := range levels {
		_, real, err := resolveModTarget(clusterPath, level.Uuid)
		if err != nil {
			items[i].err = err.Error()
			continue
		}
		items[i].real = real
		groups[real] = append(groups[real], level.Uuid)
	}
	for i := range levels {
		if items[i].err != "" {
			levels[i].ModConfigError = items[i].err
			levels[i].ModConfigWorlds = []string{levels[i].Uuid}
			continue
		}
		levels[i].ModConfigError = ""
		levels[i].ModConfigWorlds = append([]string(nil), groups[items[i].real]...)
	}
	return levels
}

func resolveModTarget(clusterPath, uuid string) (string, string, error) {
	if uuid == "" || uuid == "." || uuid == ".." || strings.ContainsAny(uuid, `/\`) {
		return "", "", fmt.Errorf("世界路径无效")
	}
	writePath := filepath.Join(clusterPath, uuid, "modoverrides.lua")
	if err := insideCluster(clusterPath, writePath); err != nil {
		return "", "", err
	}
	info, statErr := os.Lstat(writePath)
	if statErr != nil {
		if os.IsNotExist(statErr) {
			return writePath, filepath.Clean(writePath), nil
		}
		return "", "", fmt.Errorf("无法读取 %s 的模组配置", uuid)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		real, err := filepath.EvalSymlinks(writePath)
		if err != nil {
			return "", "", fmt.Errorf("无法读取 %s 的模组配置: 链接无效", uuid)
		}
		file, err := os.Open(real)
		if err != nil {
			return "", "", fmt.Errorf("无法读取 %s 的模组配置", uuid)
		}
		file.Close()
		if err = insideCluster(clusterPath, real); err != nil {
			return "", "", fmt.Errorf("%s 的模组配置指向集群之外", uuid)
		}
		return writePath, real, nil
	}
	file, err := os.Open(writePath)
	if err != nil {
		return "", "", fmt.Errorf("无法读取 %s 的模组配置", uuid)
	}
	file.Close()
	real, err := filepath.EvalSymlinks(writePath)
	if err != nil {
		return "", "", fmt.Errorf("无法读取 %s 的模组配置", uuid)
	}
	if err = insideCluster(clusterPath, real); err != nil {
		return "", "", err
	}
	return writePath, real, nil
}

func insideCluster(clusterPath, target string) error {
	rel, err := filepath.Rel(canonicalPath(clusterPath), canonicalPath(target))
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("模组配置路径超出当前集群")
	}
	return nil
}

func canonicalPath(path string) string {
	if real, err := filepath.EvalSymlinks(path); err == nil {
		return real
	}
	parent := filepath.Dir(path)
	if realParent, err := filepath.EvalSymlinks(parent); err == nil {
		return filepath.Join(realParent, filepath.Base(path))
	}
	return filepath.Clean(path)
}
