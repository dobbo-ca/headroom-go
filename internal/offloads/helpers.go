package offloads

import (
	"github.com/tidwall/gjson"
)

// filePathFromToolInput extracts the file_path from tool_input JSON, checking
// the keys in upstream's order: file_path, path, filePath, filename.
// Shared by ReadOutline, TextOffload, and LogOffload for read protection.
func filePathFromToolInput(inputJSON string) string {
	for _, key := range []string{"file_path", "path", "filePath", "filename"} {
		if v := gjson.Get(inputJSON, key); v.Exists() && v.String() != "" {
			return v.String()
		}
	}
	return ""
}
