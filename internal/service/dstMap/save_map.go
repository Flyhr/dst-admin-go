package dstMap

import (
	"bytes"
	"fmt"
	"os"
	"regexp"

	"github.com/yuin/gopher-lua/ast"
	"github.com/yuin/gopher-lua/parse"
)

var saveHeader = regexp.MustCompile(`^KLEI[ \t]+1([ \t\r\n]|$)`)

// Parse the map table without executing the save's functions or other Lua code.
func readWorldMap(filePath string) (*ast.TableExpr, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, err
	}
	if end := bytes.IndexByte(data, 0); end >= 0 {
		data = data[:end]
	}
	data = saveHeader.ReplaceAll(bytes.TrimPrefix(data, []byte("\xef\xbb\xbf")), []byte("$1"))
	chunk, err := parse.Parse(bytes.NewReader(data), filePath)
	if err != nil {
		return nil, fmt.Errorf("解析存档地图失败: %w", err)
	}
	for _, statement := range chunk {
		assignment, ok := statement.(*ast.AssignStmt)
		if !ok {
			continue
		}
		for i, target := range assignment.Lhs {
			attribute, ok := target.(*ast.AttrGetExpr)
			if !ok || i >= len(assignment.Rhs) {
				continue
			}
			object, objectOK := attribute.Object.(*ast.IdentExpr)
			key, keyOK := attribute.Key.(*ast.StringExpr)
			if !objectOK || !keyOK || object.Value != "tablefunctions" || key.Value != "map_fn" {
				continue
			}
			if function, ok := assignment.Rhs[i].(*ast.FunctionExpr); ok {
				if table := returnedTable(function.Stmts); table != nil {
					return table, nil
				}
			}
		}
	}
	// Also accept ordinary saves containing return { map = { ... } }.
	if root := returnedTable(chunk); root != nil {
		if table, ok := worldMapField(root, "map").(*ast.TableExpr); ok {
			return table, nil
		}
	}
	return nil, fmt.Errorf("存档中未找到地图数据表")
}

func returnedTable(statements []ast.Stmt) *ast.TableExpr {
	for _, statement := range statements {
		if result, ok := statement.(*ast.ReturnStmt); ok && len(result.Exprs) == 1 {
			table, _ := result.Exprs[0].(*ast.TableExpr)
			return table
		}
	}
	return nil
}

func worldMapField(table *ast.TableExpr, name string) ast.Expr {
	for _, field := range table.Fields {
		if key, ok := field.Key.(*ast.StringExpr); ok && key.Value == name {
			return field.Value
		}
	}
	return nil
}
