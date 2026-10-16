package gameConfig

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

var ErrInvalidClusterName = errors.New("房间名称不能为空、超过 100 个字符或包含控制字符")

func (p *GameConfig) GetClusterName(clusterName string) (string, error) {
	return readClusterNameFile(p.archive.ClusterIniPath(clusterName))
}

func (p *GameConfig) SaveClusterName(clusterName, name string) error {
	return saveClusterNameFile(p.archive.ClusterIniPath(clusterName), name)
}

func readClusterNameFile(path string) (string, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("读取 cluster.ini 失败: %w", err)
	}
	section := ""
	name := ""
	var state iniLineState
	for _, line := range strings.Split(string(content), "\n") {
		line = strings.TrimSuffix(line, "\r")
		if state.skips(line) {
			continue
		}
		if next, ok := iniSectionName(line); ok {
			section = next
			continue
		}
		if section == "NETWORK" {
			if _, value, ok := clusterNameLine(line); ok {
				name = strings.TrimSpace(value)
			}
		}
		state.observe(line)
	}
	return name, nil
}

func saveClusterNameFile(path, name string) error {
	name = strings.TrimSpace(name)
	if !validClusterName(name) {
		return ErrInvalidClusterName
	}
	original, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("读取 cluster.ini 失败: %w", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("读取 cluster.ini 属性失败: %w", err)
	}
	if !info.Mode().IsRegular() {
		return errors.New("cluster.ini 不是普通文件")
	}
	updated := replaceClusterName(string(original), name)
	if updated == string(original) {
		return nil
	}
	mode := info.Mode().Perm()
	if err := writeTemporaryReplacement(path+".bak", original, mode); err != nil {
		return fmt.Errorf("备份 cluster.ini 失败: %w", err)
	}
	if err := writeTemporaryReplacement(path, []byte(updated), mode); err != nil {
		return fmt.Errorf("替换 cluster.ini 失败（原文件仍在，备份为 cluster.ini.bak）: %w", err)
	}
	return nil
}

func validClusterName(name string) bool {
	if name == "" || !utf8.ValidString(name) || utf8.RuneCountInString(name) > 100 {
		return false
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func iniSectionName(line string) (string, bool) {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "[") {
		return "", false
	}
	end := strings.IndexByte(trimmed, ']')
	if end < 0 {
		return "", false
	}
	return strings.ToUpper(strings.TrimSpace(trimmed[1:end])), true
}

func clusterNameLine(line string) (string, string, bool) {
	equal := strings.IndexByte(line, '=')
	if equal < 0 || !strings.EqualFold(strings.TrimSpace(line[:equal]), "cluster_name") {
		return "", "", false
	}
	prefixEnd := equal + 1
	for prefixEnd < len(line) && (line[prefixEnd] == ' ' || line[prefixEnd] == '\t') {
		prefixEnd++
	}
	return line[:prefixEnd], line[prefixEnd:], true
}

type iniLineState struct {
	tripleQuoted bool
	continued    bool
}

func (s *iniLineState) skips(line string) bool {
	if s.tripleQuoted {
		s.tripleQuoted = !strings.Contains(line, `"""`)
		return true
	}
	if s.continued {
		s.continued = hasINIContinuation(line)
		return true
	}
	return false
}

func (s *iniLineState) observe(line string) {
	equal := strings.IndexByte(line, '=')
	if equal < 0 {
		return
	}
	key := strings.TrimSpace(line[:equal])
	if key == "" || strings.HasPrefix(key, ";") || strings.HasPrefix(key, "#") {
		return
	}
	value := strings.TrimSpace(line[equal+1:])
	s.tripleQuoted = strings.HasPrefix(value, `"""`) && strings.Count(value, `"""`) == 1
	s.continued = !s.tripleQuoted && hasINIContinuation(value)
}

func hasINIContinuation(value string) bool {
	return strings.HasSuffix(strings.TrimRight(value, " \t"), "\\")
}

func replaceClusterName(content, name string) string {
	newline := "\n"
	if strings.Contains(content, "\r\n") {
		newline = "\r\n"
	}
	var out strings.Builder
	section := ""
	sectionFound := false
	nameFound := false
	var state iniLineState
	for _, part := range strings.SplitAfter(content, "\n") {
		if part == "" {
			continue
		}
		line := strings.TrimSuffix(strings.TrimSuffix(part, "\n"), "\r")
		ending := part[len(line):]
		if state.skips(line) {
			out.WriteString(part)
			continue
		}
		if next, ok := iniSectionName(line); ok {
			if section == "NETWORK" && !nameFound {
				out.WriteString("cluster_name = " + name + newline)
				nameFound = true
			}
			section = next
			if section == "NETWORK" {
				sectionFound = true
			}
		}
		if section == "NETWORK" {
			if prefix, _, ok := clusterNameLine(line); ok {
				part = prefix + name + ending
				nameFound = true
			}
		}
		state.observe(line)
		out.WriteString(part)
	}
	if !nameFound {
		if content != "" && !strings.HasSuffix(content, "\n") {
			out.WriteString(newline)
		}
		if !sectionFound {
			out.WriteString("[NETWORK]" + newline)
		}
		out.WriteString("cluster_name = " + name + newline)
	}
	return out.String()
}

func writeTemporaryReplacement(target string, content []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(target), ".cluster-ini-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), target)
}
