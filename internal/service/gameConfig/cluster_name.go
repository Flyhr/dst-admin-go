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

var ErrInvalidClusterName = errors.New("房间名称不能为空、超过 100 个字符、包含控制字符或使用多行续行写法")

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
	updated, err := replaceClusterName(string(original), name)
	if err != nil {
		return err
	}
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
	var state iniLineState
	state.observe("cluster_name = " + name)
	return state.quote == "" && !state.continued
}

func iniSectionName(line string) (string, bool) {
	trimmed := strings.TrimSpace(strings.TrimPrefix(line, "\ufeff"))
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
	prefix, key, value, ok := iniKeyValue(line)
	if !ok || !strings.EqualFold(key, "cluster_name") {
		return "", "", false
	}
	return prefix, value, true
}

// Return the original prefix so editing one value preserves its key and spacing.
func iniKeyValue(line string) (prefix, key, value string, ok bool) {
	trimmed := strings.TrimLeftFunc(strings.TrimPrefix(line, "\ufeff"), unicode.IsSpace)
	if trimmed == "" || strings.ContainsRune(";#[", rune(trimmed[0])) {
		return "", "", "", false
	}
	start := len(line) - len(trimmed)
	quote := ""
	if strings.HasPrefix(trimmed, `"""`) {
		quote = `"""`
	} else if trimmed[0] == '"' || trimmed[0] == '`' {
		quote = trimmed[:1]
	}
	delimiter := -1
	if quote != "" {
		end := strings.Index(trimmed[len(quote):], quote)
		if end < 0 {
			return "", "", "", false
		}
		key = trimmed[len(quote) : len(quote)+end]
		after := len(quote)*2 + end
		rest := strings.TrimLeftFunc(trimmed[after:], unicode.IsSpace)
		if rest != "" && (rest[0] == '=' || rest[0] == ':') {
			delimiter = len(trimmed) - len(rest)
		}
	} else {
		delimiter = strings.IndexAny(trimmed, "=:")
		if delimiter >= 0 {
			key = strings.TrimSpace(trimmed[:delimiter])
		}
	}
	if delimiter < 0 || key == "" {
		return "", "", "", false
	}
	prefixEnd := start + delimiter + 1
	for prefixEnd < len(line) && (line[prefixEnd] == ' ' || line[prefixEnd] == '\t') {
		prefixEnd++
	}
	return line[:prefixEnd], key, line[prefixEnd:], true
}

type iniLineState struct {
	quote     string
	continued bool
}

func (s *iniLineState) skips(line string) bool {
	if s.quote != "" {
		if strings.Contains(line, s.quote) {
			s.quote = ""
		}
		return true
	}
	if s.continued {
		s.continued = hasINIContinuation(line)
		return true
	}
	return false
}

func (s *iniLineState) observe(line string) {
	_, _, value, ok := iniKeyValue(line)
	if !ok {
		return
	}
	value = strings.TrimSpace(value)
	for _, quote := range []string{`"""`, "`"} {
		if strings.HasPrefix(value, quote) {
			if !strings.Contains(value[len(quote):], quote) {
				s.quote = quote
			}
			return
		}
	}
	s.continued = hasINIContinuation(value)
}

func hasINIContinuation(value string) bool {
	return strings.HasSuffix(strings.TrimRight(value, " \t"), "\\")
}

func replaceClusterName(content, name string) (string, error) {
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
				state.observe(line)
				if state.quote != "" || state.continued {
					return "", errors.New("现有房间名称使用多行写法，请先在文本编辑器中改为单行；原文件未修改")
				}
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
	return out.String(), nil
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
