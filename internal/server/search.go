package server

import (
	"bufio"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// searchIgnore names directory entries skipped during server-side search.
var searchIgnore = map[string]bool{
	".git": true, "node_modules": true, ".DS_Store": true,
}

type fileGlobRequest struct {
	Pattern string `json:"pattern"`
	Path    string `json:"path"`
	Limit   int    `json:"limit"`
}

type fileGlobResponse struct {
	Paths []string `json:"paths"`
}

func (s *Server) fileGlob(response http.ResponseWriter, request *http.Request) {
	workspace, ok := s.lookupWorkspace(response, request)
	if !ok {
		return
	}
	var body fileGlobRequest
	if err := decodeBody(request, &body); err != nil {
		writeAPIError(response, err)
		return
	}
	if body.Pattern == "" {
		writeError(response, http.StatusBadRequest, "invalid_request", "pattern is required")
		return
	}
	searchRoot := body.Path
	if searchRoot == "" {
		searchRoot = workspace.RootPath
	}
	root, ok := s.resolveWorkspacePath(response, request, pathRequest{Path: searchRoot})
	if !ok {
		return
	}
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		writeError(response, http.StatusBadRequest, "invalid_request", "search path is not a directory: "+searchRoot)
		return
	}

	limit := body.Limit
	if limit <= 0 {
		limit = 200
	}
	matcher := compileGlob(body.Pattern)

	var paths []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable entries
		}
		if entry.IsDir() {
			if searchIgnore[entry.Name()] && path != root {
				return filepath.SkipDir
			}
			return nil
		}
		if path == root {
			return nil
		}
		relative := filepath.ToSlash(strings.TrimPrefix(path, root+string(filepath.Separator)))
		if matcher(relative) || matcher(filepath.Base(path)) {
			paths = append(paths, path)
			if len(paths) >= limit {
				return fs.SkipAll
			}
		}
		return nil
	})
	if err != nil {
		writeAPIError(response, err)
		return
	}
	if paths == nil {
		paths = []string{}
	}
	writeJSON(response, http.StatusOK, fileGlobResponse{Paths: paths})
}

// compileGlob converts a glob with *, **, and ? into a matcher over
// slash-separated relative paths.
func compileGlob(pattern string) func(string) bool {
	expression := regexp.QuoteMeta(pattern)
	expression = strings.ReplaceAll(expression, `\*\*`, `.*`)
	expression = strings.ReplaceAll(expression, `\*`, `[^/]*`)
	expression = strings.ReplaceAll(expression, `\?`, `[^/]`)
	matcher := regexp.MustCompile(`^` + expression + `$`)
	return func(path string) bool { return matcher.MatchString(path) }
}

type grepLine struct {
	N     int    `json:"n"`
	Text  string `json:"text"`
	Match bool   `json:"match"`
}

type grepMatch struct {
	Path       string     `json:"path"`
	LineNumber int        `json:"lineNumber"`
	LineText   string     `json:"lineText"`
	Lines      []grepLine `json:"lines"`
}

type fileGrepRequest struct {
	Pattern    string `json:"pattern"`
	Path       string `json:"path"`
	Glob       string `json:"glob"`
	IgnoreCase bool   `json:"ignoreCase"`
	Literal    bool   `json:"literal"`
	Context    int    `json:"context"`
	Limit      int    `json:"limit"`
}

type fileGrepResponse struct {
	Matches      []grepMatch `json:"matches"`
	LimitReached bool        `json:"limitReached"`
}

func (s *Server) fileGrep(response http.ResponseWriter, request *http.Request) {
	workspace, ok := s.lookupWorkspace(response, request)
	if !ok {
		return
	}
	var body fileGrepRequest
	if err := decodeBody(request, &body); err != nil {
		writeAPIError(response, err)
		return
	}
	if body.Pattern == "" {
		writeError(response, http.StatusBadRequest, "invalid_request", "pattern is required")
		return
	}

	var matcher func(string) bool
	if body.Literal {
		needle := body.Pattern
		if body.IgnoreCase {
			needle = strings.ToLower(needle)
			matcher = func(line string) bool { return strings.Contains(strings.ToLower(line), needle) }
		} else {
			matcher = func(line string) bool { return strings.Contains(line, needle) }
		}
	} else {
		flags := ""
		if body.IgnoreCase {
			flags = "(?i)"
		}
		expression, err := regexp.Compile(flags + body.Pattern)
		if err != nil {
			writeError(response, http.StatusBadRequest, "invalid_request", "invalid pattern: "+err.Error())
			return
		}
		matcher = expression.MatchString
	}

	searchRoot := body.Path
	if searchRoot == "" {
		searchRoot = workspace.RootPath
	}
	root, ok := s.resolveWorkspacePath(response, request, pathRequest{Path: searchRoot})
	if !ok {
		return
	}
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		writeError(response, http.StatusBadRequest, "invalid_request", "search path is not a directory: "+searchRoot)
		return
	}

	limit := body.Limit
	if limit <= 0 {
		limit = 100
	}
	context := max(body.Context, 0)
	var globMatcher func(string) bool
	if body.Glob != "" {
		globMatcher = compileGlob(body.Glob)
	}

	result := fileGrepResponse{Matches: []grepMatch{}}
	_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if entry.IsDir() {
			if searchIgnore[entry.Name()] && path != root {
				return filepath.SkipDir
			}
			return nil
		}
		if path == root || !isSearchableFile(path, globMatcher) {
			return nil
		}
		if matches, matched := grepFile(path, root, matcher, context, limit-len(result.Matches)); matched {
			result.Matches = append(result.Matches, matches...)
		}
		if len(result.Matches) >= limit {
			result.LimitReached = true
			return fs.SkipAll
		}
		return nil
	})
	writeJSON(response, http.StatusOK, result)
}

func isSearchableFile(path string, globMatcher func(string) bool) bool {
	if globMatcher != nil {
		return globMatcher(filepath.ToSlash(path)) || globMatcher(filepath.Base(path))
	}
	return !looksBinary(path)
}

// looksBinary peeks at the file to skip non-text content.
func looksBinary(path string) bool {
	file, err := os.Open(path)
	if err != nil {
		return true
	}
	defer file.Close()
	head := make([]byte, 1024)
	read, _ := file.Read(head)
	if read == 0 {
		return false
	}
	return strings.ContainsRune(string(head[:read]), 0)
}

func grepFile(path, root string, matcher func(string) bool, context, budget int) ([]grepMatch, bool) {
	file, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	defer file.Close()

	var lines []string
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return nil, false
	}

	var matches []grepMatch
	for index, line := range lines {
		if !matcher(line) {
			continue
		}
		match := grepMatch{Path: path, LineNumber: index + 1, LineText: line, Lines: []grepLine{}}
		if context > 0 {
			start := max(index+1-context, 1)
			end := min(index+1+context, len(lines))
			for n := start; n <= end; n++ {
				match.Lines = append(match.Lines, grepLine{N: n, Text: lines[n-1], Match: n == index+1})
			}
		}
		matches = append(matches, match)
		if len(matches) >= budget {
			break
		}
	}
	return matches, len(matches) > 0
}
