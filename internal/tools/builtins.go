// Package tools provides function-calling tool registry, built-in tools,
// and provider schema conversion.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// RegisterBuiltins registers all built-in tools with the registry.
func RegisterBuiltins(registry *Registry) error {
	if err := registry.Register(ToolDef{
		Name:        "get-current-time",
		Description: "Returns the current UTC date and time.",
		Schema:      mustParse(`{"type":"object","properties":{}}`),
		Handler:     getCurrentTimeHandler,
		Timeout:     50 * time.Millisecond,
		CacheTTL:    5 * time.Second,
	}); err != nil {
		return err
	}

	if err := registry.Register(ToolDef{
		Name:        "read-file",
		Description: "Reads the contents of a local file.",
		Schema: mustParse(`{
			"type": "object",
			"properties": {
				"path": {
					"type": "string",
					"description": "Absolute file path to read"
				},
				"offset": {
					"type": "integer",
					"description": "Byte offset to start reading (default: 0)"
				},
				"length": {
					"type": "integer",
					"description": "Maximum bytes to read (default: 65536)"
				}
			},
			"required": ["path"]
		}`),
		Handler:  readFileHandler,
		Timeout:  2 * time.Second,
		CacheTTL: 30 * time.Second,
	}); err != nil {
		return err
	}

	if err := registry.Register(ToolDef{
		Name:        "list-directory",
		Description: "Lists files and directories at a given path.",
		Schema: mustParse(`{
			"type": "object",
			"properties": {
				"path": {
					"type": "string",
					"description": "Directory path to list"
				}
			},
			"required": ["path"]
		}`),
		Handler:  listDirHandler,
		Timeout:  2 * time.Second,
		CacheTTL: 5 * time.Second,
	}); err != nil {
		return err
	}

	return nil
}

func getCurrentTimeHandler(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
	now := time.Now().UTC()
	return json.Marshal(map[string]interface{}{
		"utc_iso":   now.Format(time.RFC3339),
		"unix":      strconv.FormatInt(now.Unix(), 10),
		"timezone":  "UTC",
		"date":      now.Format("2006-01-02"),
		"time":      now.Format("15:04:05"),
		"weekday":   now.Weekday().String(),
		"day_of_year": now.YearDay(),
	})
}

func readFileHandler(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
	var req struct {
		Path   string `json:"path"`
		Offset int    `json:"offset"`
		Length int    `json:"length"`
	}

	if err := json.Unmarshal(args, &req); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}

	if req.Path == "" {
		return nil, fmt.Errorf("path is required")
	}

	// Security: prevent path traversal
	if strings.Contains(req.Path, "..") {
		return nil, fmt.Errorf("path traversal not allowed")
	}

	// Default length
	if req.Length == 0 {
		req.Length = 65536
	}

	data, err := os.ReadFile(req.Path)
	if err != nil {
		return nil, fmt.Errorf("failed to read file: %w", err)
	}

	// Apply offset and length
	if req.Offset > len(data) {
		return nil, fmt.Errorf("offset beyond file length")
	}

	end := req.Offset + req.Length
	if end > len(data) {
		end = len(data)
	}

	return json.Marshal(map[string]interface{}{
		"path":      req.Path,
		"size":      len(data),
		"read":      end - req.Offset,
		"content":   string(data[req.Offset:end]),
		"truncated": end < len(data),
	})
}

func listDirHandler(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
	var req struct {
		Path string `json:"path"`
	}

	if err := json.Unmarshal(args, &req); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}

	if req.Path == "" {
		req.Path = "."
	}

	// Security: prevent path traversal
	if strings.Contains(req.Path, "..") {
		return nil, fmt.Errorf("path traversal not allowed")
	}

	entries, err := os.ReadDir(req.Path)
	if err != nil {
		return nil, fmt.Errorf("failed to read directory: %w", err)
	}

	type Entry struct {
		Name    string `json:"name"`
		IsDir   bool   `json:"is_dir"`
		IsFile  bool   `json:"is_file"`
		Size    int64  `json:"size"`
		ModTime string `json:"mod_time"`
	}

	result := make([]Entry, 0, len(entries))
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			continue
		}
		result = append(result, Entry{
			Name:    entry.Name(),
			IsDir:   entry.IsDir(),
			IsFile:  entry.Type().IsRegular(),
			Size:    info.Size(),
			ModTime: info.ModTime().Format(time.RFC3339),
		})
	}

	return json.Marshal(map[string]interface{}{
		"path":    req.Path,
		"count":   len(result),
		"entries": result,
	})
}

func mustParse(schema string) json.RawMessage {
	return json.RawMessage(schema)
}
