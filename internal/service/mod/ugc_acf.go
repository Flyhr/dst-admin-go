package mod

import (
	"os"
	"sort"
	"strconv"
	"strings"
)

// parseWorkshopItemsInstalled 只解析 ACF 的 WorkshopItemsInstalled 段。
//
// 旧实现从段头开始一直解析到文件末尾，会把 WorkshopItemDetails（详情缓存）里的
// 条目也当成“已安装”，导致面板显示一些实际未安装的模组；删除文件后列表也不消失。
func parseWorkshopItemsInstalled(filePath string) map[string]WorkshopItem {
	raw, err := os.ReadFile(filePath)
	if err != nil {
		return nil
	}
	text := strings.ReplaceAll(string(raw), "\r\n", "\n")
	lines := strings.Split(text, "\n")

	items := make(map[string]WorkshopItem)
	inSection := false
	started := false
	depth := 0
	currentID := ""
	var currentItem WorkshopItem

	flush := func() {
		if currentID != "" {
			items[currentID] = currentItem
			currentID = ""
			currentItem = WorkshopItem{}
		}
	}

	for _, rawLine := range lines {
		line := strings.TrimSpace(rawLine)
		if !inSection {
			if line == `"WorkshopItemsInstalled"` {
				inSection = true
			}
			continue
		}
		if !started {
			if line == "{" {
				started = true
				depth = 1
			}
			continue
		}
		if depth == 1 {
			if line == "}" {
				flush()
				break
			}
			if id, ok := parseACFItemID(line); ok {
				flush()
				currentID = id
				continue
			}
		}
		if line == "{" {
			depth++
			continue
		}
		if line == "}" {
			depth--
			continue
		}
		if depth < 2 {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 || !strings.HasPrefix(fields[0], `"`) {
			continue
		}
		key := strings.Trim(fields[0], `"`)
		value := strings.Trim(fields[1], `"`)
		switch key {
		case "timeupdated":
			currentItem.TimeUpdated, _ = strconv.ParseInt(value, 10, 64)
		case "manifest":
			currentItem.Manifest = value
		case "ugchandle":
			currentItem.Ugchandle = value
		}
	}
	return items
}

// sortedWorkshopItemIDs 返回稳定的工坊条目顺序：最后更新时间从新到旧，
// 时间相同时按 id 升序，避免直接遍历 map 导致每次查询顺序都变化。
func sortedWorkshopItemIDs(items map[string]WorkshopItem) []string {
	ids := make([]string, 0, len(items))
	for id := range items {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		left, right := items[ids[i]], items[ids[j]]
		if left.TimeUpdated == right.TimeUpdated {
			return ids[i] < ids[j]
		}
		return left.TimeUpdated > right.TimeUpdated
	})
	return ids
}

// sortUgcDetailsByDownloadTime 返回按“下载时间”从新到旧排序的副本。
// 下载时间相同的条目保持原有相对顺序（调用方已先按最后更新时间排好序，作为稳定兜底）。
func sortUgcDetailsByDownloadTime(details []WorkshopItemDetail) []WorkshopItemDetail {
	sorted := append([]WorkshopItemDetail(nil), details...)
	sort.SliceStable(sorted, func(i, j int) bool {
		return sorted[i].DownloadedAt > sorted[j].DownloadedAt
	})
	return sorted
}

// parseACFItemID 判断一行是否为 ACF 中的工坊条目 id（形如 "1234567890"）。
func parseACFItemID(line string) (string, bool) {
	if len(line) < 3 || !strings.HasPrefix(line, `"`) || !strings.HasSuffix(line, `"`) {
		return "", false
	}
	id := strings.Trim(line, `"`)
	if id == "" {
		return "", false
	}
	if _, err := strconv.ParseUint(id, 10, 64); err != nil {
		return "", false
	}
	return id, true
}

// removeACFWorkshopItem 从 ACF 的 WorkshopItemsInstalled 与 WorkshopItemDetails
// 两个段中删除指定工坊条目，保持其它条目与文件原有换行格式。
// 返回是否实际发生修改；文件不存在或条目不存在时返回 false, nil。
func removeACFWorkshopItem(filePath, workshopID string) (bool, error) {
	if workshopID == "" {
		return false, nil
	}
	raw, err := os.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	text := string(raw)
	newline := "\n"
	if strings.Contains(text, "\r\n") {
		newline = "\r\n"
	}
	normalized := strings.ReplaceAll(text, "\r\n", "\n")
	lines := strings.Split(normalized, "\n")
	targetSections := map[string]bool{
		`"WorkshopItemsInstalled"`: true,
		`"WorkshopItemDetails"`:    true,
	}
	idLine := `"` + workshopID + `"`

	out := make([]string, 0, len(lines))
	changed := false
	i := 0
	for i < len(lines) {
		line := lines[i]
		sectionHeader := strings.TrimSpace(line)
		if !targetSections[sectionHeader] {
			out = append(out, line)
			i++
			continue
		}
		out = append(out, line)
		i++
		if i >= len(lines) || strings.TrimSpace(lines[i]) != "{" {
			continue
		}
		out = append(out, lines[i])
		i++
		depth := 1
		for i < len(lines) && depth > 0 {
			current := lines[i]
			trimmed := strings.TrimSpace(current)
			if depth == 1 && trimmed == idLine {
				// 跳过整个 "<id>" { ... } 块。
				changed = true
				i++
				blockDepth := 0
			skipBlock:
				for i < len(lines) {
					blockLine := strings.TrimSpace(lines[i])
					switch blockLine {
					case "{":
						blockDepth++
					case "}":
						blockDepth--
						if blockDepth <= 0 {
							i++
							break skipBlock
						}
					}
					i++
				}
				continue
			}
			switch trimmed {
			case "{":
				depth++
			case "}":
				depth--
			}
			out = append(out, current)
			i++
		}
	}

	if !changed {
		return false, nil
	}
	newText := strings.Join(out, "\n")
	if newline == "\r\n" {
		newText = strings.ReplaceAll(newText, "\n", "\r\n")
	}
	perm := os.FileMode(0644)
	if info, statErr := os.Stat(filePath); statErr == nil {
		perm = info.Mode().Perm()
	}
	return true, os.WriteFile(filePath, []byte(newText), perm)
}
