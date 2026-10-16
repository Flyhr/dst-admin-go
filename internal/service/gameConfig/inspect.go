package gameConfig

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-ini/ini"
)

type ClusterConfigFile struct {
	Name     string `json:"name"`
	Exists   bool   `json:"exists"`
	Content  string `json:"content,omitempty"`
	Redacted bool   `json:"redacted,omitempty"`
	Error    string `json:"error,omitempty"`
}

type ClusterConfigInspection struct {
	Path  string              `json:"path"`
	Files []ClusterConfigFile `json:"files"`
}

func (p *GameConfig) InspectClusterConfig(clusterName string) (ClusterConfigInspection, error) {
	return inspectClusterFiles(p.archive.ClusterPath(clusterName))
}

// Inspect existing files directly; level initialization and game operations are not used.
func inspectClusterFiles(clusterPath string) (ClusterConfigInspection, error) {
	view := ClusterConfigInspection{Path: clusterPath, Files: []ClusterConfigFile{}}
	info, err := os.Stat(clusterPath)
	if err != nil {
		return view, err
	}
	if !info.IsDir() {
		return view, fmt.Errorf("集群路径不是文件夹: %s", clusterPath)
	}
	// Fixed filenames keep this endpoint independent of client-supplied file paths.
	names := []string{
		"cluster.ini", "adminlist.txt",
		"Master/server.ini", "Master/worldgenoverride.lua", "Master/leveldataoverride.lua", "Master/modoverrides.lua",
		"Caves/server.ini", "Caves/worldgenoverride.lua", "Caves/leveldataoverride.lua", "Caves/modoverrides.lua",
	}
	for _, name := range names {
		file := ClusterConfigFile{Name: name}
		content, err := readInspectionFile(filepath.Join(clusterPath, filepath.FromSlash(name)))
		if err != nil {
			if !os.IsNotExist(err) {
				file.Error = err.Error()
			}
		} else {
			file.Exists = true
			file.Content = string(content)
			if filepath.Ext(name) == ".ini" {
				file.Content, file.Redacted, err = redactInspectionINI(content)
				if err != nil {
					file.Content = ""
					file.Error = "INI 解析失败，请在文本编辑器中检查该文件"
				}
			}
		}
		view.Files = append(view.Files, file)
	}
	return view, nil
}

func readInspectionFile(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	const limit = 1024 * 1024
	content, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err == nil && len(content) > limit {
		return nil, fmt.Errorf("文件超过 1 MiB，请使用文本编辑器查看")
	}
	return content, err
}

func redactInspectionINI(content []byte) (string, bool, error) {
	_, err := ini.LoadSources(ini.LoadOptions{AllowShadows: true}, content)
	if err != nil {
		return "", false, err
	}
	var output strings.Builder
	redacted := false
	var secretState iniLineState
	for _, part := range strings.SplitAfter(string(content), "\n") {
		if part == "" {
			continue
		}
		line := strings.TrimSuffix(strings.TrimSuffix(part, "\n"), "\r")
		ending := part[len(line):]
		if secretState.skips(line) {
			output.WriteString(ending)
			continue
		}
		prefix, key, _, ok := iniKeyValue(line)
		if ok {
			switch strings.ToLower(key) {
			case "cluster_password", "cluster_key", "cluster_token":
				secretState.observe(line)
				output.WriteString(prefix + "[已隐藏]" + ending)
				redacted = true
				continue
			}
		}
		output.WriteString(part)
	}
	return output.String(), redacted, nil
}
