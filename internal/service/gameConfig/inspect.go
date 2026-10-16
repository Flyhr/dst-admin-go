package gameConfig

import (
	"bytes"
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
	cfg, err := ini.LoadSources(ini.LoadOptions{AllowShadows: true}, content)
	if err != nil {
		return "", false, err
	}
	redacted := false
	for _, section := range cfg.Sections() {
		names := append([]string(nil), section.KeyStrings()...)
		for _, name := range names {
			switch strings.ToLower(name) {
			case "cluster_password", "cluster_key", "cluster_token":
				// Recreate the key so duplicate and multiline values are also removed.
				section.DeleteKey(name)
				if _, err := section.NewKey(name, "[已隐藏]"); err != nil {
					return "", false, err
				}
				redacted = true
			}
		}
	}
	if !redacted {
		return string(content), false, nil
	}
	var output bytes.Buffer
	_, err = cfg.WriteTo(&output)
	return output.String(), true, err
}
