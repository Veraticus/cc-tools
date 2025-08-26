package statusline

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Cache provides caching functionality for statusline data
type Cache struct {
	dir       string
	duration  time.Duration
	fileReader FileReader
}

// NewCache creates a new cache instance
func NewCache(dir string, duration time.Duration, fileReader FileReader) *Cache {
	if dir == "" {
		dir = "/dev/shm"
	}
	if duration == 0 {
		duration = 20 * time.Second
	}
	return &Cache{
		dir:        dir,
		duration:   duration,
		fileReader: fileReader,
	}
}

// Get retrieves cached data for a given key
func (c *Cache) Get(key string) (*CachedData, bool) {
	cacheFile := filepath.Join(c.dir, fmt.Sprintf("claude_statusline_%s", key))
	
	// Check if file exists
	if !c.fileReader.Exists(cacheFile) {
		return nil, false
	}
	
	// Check age
	modTime, err := c.fileReader.ModTime(cacheFile)
	if err != nil {
		return nil, false
	}
	
	if time.Since(modTime) > c.duration {
		return nil, false
	}
	
	// Read and parse
	content, err := c.fileReader.ReadFile(cacheFile)
	if err != nil {
		return nil, false
	}
	
	var data CachedData
	if err := json.Unmarshal(content, &data); err != nil {
		return nil, false
	}
	
	return &data, true
}

// Set stores data in the cache
func (c *Cache) Set(key string, data *CachedData) error {
	cacheFile := filepath.Join(c.dir, fmt.Sprintf("claude_statusline_%s", key))
	
	// Write in bash-compatible format (like the bash version does)
	var content strings.Builder
	content.WriteString("# Cached statusline data\n")
	content.WriteString(fmt.Sprintf("MODEL_DISPLAY=\"%s\"\n", data.ModelDisplay))
	content.WriteString(fmt.Sprintf("CURRENT_DIR=\"%s\"\n", data.CurrentDir))
	content.WriteString(fmt.Sprintf("TRANSCRIPT_PATH=\"%s\"\n", data.TranscriptPath))
	content.WriteString(fmt.Sprintf("GIT_BRANCH=\"%s\"\n", data.GitBranch))
	content.WriteString(fmt.Sprintf("GIT_STATUS=\"%s\"\n", data.GitStatus))
	content.WriteString(fmt.Sprintf("K8S_CONTEXT=\"%s\"\n", data.K8sContext))
	content.WriteString(fmt.Sprintf("INPUT_TOKENS=\"%d\"\n", data.InputTokens))
	content.WriteString(fmt.Sprintf("OUTPUT_TOKENS=\"%d\"\n", data.OutputTokens))
	content.WriteString(fmt.Sprintf("CONTEXT_LENGTH=\"%d\"\n", data.ContextLength))
	content.WriteString(fmt.Sprintf("HOSTNAME=\"%s\"\n", data.Hostname))
	content.WriteString(fmt.Sprintf("DEVSPACE=\"%s\"\n", data.Devspace))
	content.WriteString(fmt.Sprintf("DEVSPACE_SYMBOL=\"%s\"\n", data.DevspaceSymbol))
	content.WriteString(fmt.Sprintf("RAW_TERM_WIDTH=\"%d\"\n", data.TermWidth))
	
	return os.WriteFile(cacheFile, []byte(content.String()), 0600)
}