package wiki

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	wikiv1 "git.noncepad.com/pkg/optimizer/gen/wiki/v1"
	"github.com/bmatcuk/doublestar/v4"
	"github.com/cloudwego/eino/adk/middlewares/filesystem"
)

type FilesystemWrapper interface {
	// List file info in a directory
	LsInfo(ctx context.Context, req *filesystem.LsInfoRequest) ([]filesystem.FileInfo, error)

	// Read file content, supports line offset and limit
	Read(ctx context.Context, req *filesystem.ReadRequest) (*filesystem.FileContent, error)

	// Search for matching content in files
	GrepRaw(ctx context.Context, req *filesystem.GrepRequest) ([]filesystem.GrepMatch, error)

	// Match files by glob pattern
	GlobInfo(ctx context.Context, req *filesystem.GlobInfoRequest) ([]filesystem.FileInfo, error)

	// Write file content
	Write(ctx context.Context, req *filesystem.WriteRequest) error

	// Edit file content (string replacement)
	Edit(ctx context.Context, req *filesystem.EditRequest) error
}

type internalFS struct {
	web string
	c   *Client
}

func (c *Client) FileSytem(ctx context.Context, web string) FilesystemWrapper {
	in := new(internalFS)
	in.c = c
	in.web = web
	return in
}

// hierarchyNode is one topic's place in the fake filesystem this file
// builds out of a web's topics: the parent it declares via TOPICPARENT
// (ParentTopic/parent_topic throughout the wiki API), plus its size/
// modtime - everything ListTopics now returns per topic (see Topic's
// own doc comment) except body content, which GrepRaw fetches
// separately and only for the topics it actually needs to search (see
// grepCandidateBodies) - unlike LsInfo (its own single-level ListTopics
// calls) or Read/Write/Edit (GetTopicSource for one topic), GrepRaw and
// GlobInfo need every topic under a subtree just to know what's there,
// but GlobInfo never needed content at all, and GrepRaw only needs it
// for topics that might actually match.
type hierarchyNode struct {
	name      string
	parent    string
	size      int64
	timestamp int64
}

// hierarchy fetches every topic in in.web via one unfiltered ListTopics
// call and returns two views over the result: every topic by name, and
// every topic's direct children by parent name ("" for the root). A
// topic whose declared parent isn't itself a topic in this web (a
// dangling TOPICPARENT - nothing enforces referential integrity here,
// see db/schema.sql's parent_name column) is attached to the root
// instead of silently becoming unreachable from any GlobInfo/GrepRaw
// walk that starts there.
func (in *internalFS) hierarchy(ctx context.Context) (byName map[string]*hierarchyNode, children map[string][]string, err error) {
	list, err := in.c.wiki.ListTopics(ctx, &wikiv1.ListTopicsRequest{Web: in.web})
	if err != nil {
		return nil, nil, fmt.Errorf("list topics in %s: %w", in.web, err)
	}

	byName = make(map[string]*hierarchyNode, len(list.GetTopics()))
	for _, t := range list.GetTopics() {
		byName[t.GetName()] = &hierarchyNode{
			name:      t.GetName(),
			parent:    t.GetParentTopic(),
			size:      t.GetBodySize(),
			timestamp: t.GetTimestamp(),
		}
	}

	children = make(map[string][]string)
	for name, node := range byName {
		parent := node.parent
		if parent != "" {
			if _, ok := byName[parent]; !ok {
				parent = "" // dangling TOPICPARENT - surface at root
			}
		}
		children[parent] = append(children[parent], name)
	}
	for _, names := range children {
		sort.Strings(names)
	}
	return byName, children, nil
}

// topicName returns the topic a filesystem path refers to: the path's
// last segment. Wiki topic names are already unique within a web (the
// natural key GetTopicSource/PutTopic address a topic by), so any
// leading segments in a multi-part path like "/Book/Account" are purely
// cosmetic hierarchy breadcrumbs, not needed to disambiguate identity -
// "/Account" and "/Book/Account" name the same topic. "" (the root) has
// no topic and returns "".
func topicName(path string) string {
	path = strings.TrimSuffix(path, "/")
	if path == "" || path == "." {
		return ""
	}
	return filepath.Base(path)
}

// parentName returns the topic named by path's parent directory, "" for
// a path directly under the root. Used by Write to decide the
// TOPICPARENT of a topic addressed by a nested path - see Write's own
// doc comment.
func parentName(path string) string {
	dir := filepath.Dir(strings.TrimSuffix(path, "/"))
	return topicName(dir)
}

func formatModTime(unixSeconds int64) string {
	return time.Unix(unixSeconds, 0).UTC().Format(time.RFC3339)
}

// fullPath reconstructs a topic's full slash-separated path from the
// web root by walking its TOPICPARENT chain upward. Cycle-safe: a
// parent chain that loops back on itself (nothing prevents that at the
// data level) stops once a name would be visited twice, rather than
// looping forever.
func fullPath(name string, byName map[string]*hierarchyNode) string {
	segs := []string{name}
	visited := map[string]bool{name: true}
	cur := name
	for {
		node, ok := byName[cur]
		if !ok || node.parent == "" || visited[node.parent] {
			break
		}
		segs = append([]string{node.parent}, segs...)
		visited[node.parent] = true
		cur = node.parent
	}
	return "/" + strings.Join(segs, "/")
}

// relativePath is fullPath, but stopping at base instead of the web
// root - the path LsInfo/GrepRaw/GlobInfo report for a topic found
// while walking under a given base path.
func relativePath(name, base string, byName map[string]*hierarchyNode) string {
	segs := []string{name}
	visited := map[string]bool{name: true}
	cur := name
	for {
		node, ok := byName[cur]
		if !ok || node.parent == "" || node.parent == base || visited[node.parent] {
			break
		}
		segs = append([]string{node.parent}, segs...)
		visited[node.parent] = true
		cur = node.parent
	}
	return strings.Join(segs, "/")
}

// descendants returns base's own topic name (if any) plus every topic
// reachable from base through the parent/child index, so grepping/
// globbing a topic covers that topic's own content in addition to
// everything nested under it - a topic is simultaneously a "file" and,
// if it has children, a "directory" in this backend (see LsInfo).
// base == "" (the root) returns every topic in the web.
func descendants(base string, children map[string][]string) []string {
	var out []string
	var walk func(name string)
	walk = func(name string) {
		if name != "" {
			out = append(out, name)
		}
		for _, child := range children[name] {
			walk(child)
		}
	}
	walk(base)
	return out
}

// resolveBaseTopic resolves path's own topic name (topicName) against
// byName (in.hierarchy's already-fetched index) for GlobInfo/GrepRaw's
// own "does this base path exist" check - "" (the root) always
// resolves, matching topicName's own "" == root convention. Falls back
// to one trailing extension stripped, tried only after the exact name
// fails, the same hallucinated-extension leniency Read/LsInfo already
// apply (see Read's own doc comment) - a model scoping a glob/grep to
// what it thinks is a real file/directory path (e.g.
// "/wiki/Main/TestEditV1.md") naturally appends an extension a wiki
// topic name never actually has.
func resolveBaseTopic(path string, byName map[string]*hierarchyNode) (string, error) {
	base := topicName(path)
	if base == "" {
		return "", nil
	}
	if _, ok := byName[base]; ok {
		return base, nil
	}
	if stripped := strings.TrimSuffix(base, filepath.Ext(base)); stripped != "" && stripped != base {
		if _, ok := byName[stripped]; ok {
			return stripped, nil
		}
	}
	return "", fmt.Errorf("path not found: %s", path)
}

// topicExists reports whether topic exists in in.web, via the same
// exact, single-topic ListTopics filter LsInfo's own doc comment
// describes.
func (in *internalFS) topicExists(ctx context.Context, topic string) (bool, error) {
	exact, err := in.c.wiki.ListTopics(ctx, &wikiv1.ListTopicsRequest{
		Web:    in.web,
		Filter: &wikiv1.ListTopicsRequest_Topic{Topic: topic},
	})
	if err != nil {
		return false, err
	}
	return len(exact.GetTopics()) > 0, nil
}

// List file info in a directory
func (in *internalFS) LsInfo(ctx context.Context, req *filesystem.LsInfoRequest) ([]filesystem.FileInfo, error) {
	parent := topicName(req.Path)

	// Unlike GrepRaw/GlobInfo (which need every topic under a subtree,
	// so they still use index()'s full-web scan), LsInfo only ever needs
	// one level: does the requested directory-topic exist, and what are
	// its direct children. Both are single, targeted ListTopics queries
	// (an exact "topic" filter, then a "parent" filter - see
	// ListTopicsRequest's own doc comment) - no GetTopicSource calls,
	// and no need to pull the rest of the web to answer either question.
	if parent != "" {
		exists, err := in.topicExists(ctx, parent)
		if err != nil {
			return nil, fmt.Errorf("look up %s: %w", req.Path, err)
		}
		if !exists {
			// Same hallucinated-trailing-extension leniency Read already
			// applies (see its own doc comment) - a model calling ls on
			// what it thinks is a real file/directory path (e.g.
			// "/wiki/Main/TestEditV1.md") naturally appends an
			// extension a wiki topic name never actually has. Retried
			// only after the exact name fails, so a topic whose real
			// name genuinely contains a dot still resolves on the first
			// attempt.
			if stripped := strings.TrimSuffix(parent, filepath.Ext(parent)); stripped != "" && stripped != parent {
				if strippedExists, err := in.topicExists(ctx, stripped); err == nil && strippedExists {
					parent, exists = stripped, true
				}
			}
		}
		if !exists {
			return nil, fmt.Errorf("directory not found: %s", req.Path)
		}
	}

	children, err := in.c.wiki.ListTopics(ctx, &wikiv1.ListTopicsRequest{
		Web:    in.web,
		Filter: &wikiv1.ListTopicsRequest_Parent{Parent: parent},
	})
	if err != nil {
		return nil, fmt.Errorf("list children of %s: %w", req.Path, err)
	}

	list := make([]filesystem.FileInfo, 0, len(children.GetTopics()))
	for _, t := range children.GetTopics() {
		list = append(list, filesystem.FileInfo{
			Path:       filepath.Join(req.Path, t.GetName()),
			IsDir:      t.GetHasChildren(),
			Size:       t.GetBodySize(),
			ModifiedAt: formatModTime(t.GetTimestamp()),
		})
	}
	return list, nil
}

// sliceLines applies ReadRequest's 1-based Offset/Limit semantics to
// content - Offset < 1 means "start from line 1", Limit <= 0 means "no
// limit" - the same semantics eino's own InMemoryBackend.Read documents
// (adk/filesystem/backend_inmemory.go), just implemented by splitting
// the whole body into lines rather than that implementation's streaming
// byte-scan (a fine tradeoff for a wiki topic's typical size).
func sliceLines(content string, offset, limit int) string {
	if offset < 1 {
		offset = 1
	}
	lines := strings.Split(content, "\n")
	start := offset - 1
	if start >= len(lines) {
		return ""
	}
	end := len(lines)
	if limit > 0 && start+limit < end {
		end = start + limit
	}
	return strings.Join(lines[start:end], "\n")
}

// Read file content, supports line offset and limit
func (in *internalFS) Read(ctx context.Context, req *filesystem.ReadRequest) (*filesystem.FileContent, error) {
	name := topicName(req.FilePath)
	if name == "" {
		return nil, fmt.Errorf("not a file: %s", req.FilePath)
	}
	src, err := in.c.wiki.GetTopicSource(ctx, &wikiv1.GetTopicRequest{Web: in.web, Topic: name})
	if err != nil {
		// A model driving this tool (e.g. client/hedgefund's own
		// read-only wiki browsing tools) naturally thinks of read_file
		// paths as real files with extensions - a real, live-confirmed
		// failure mode: it asked for "/wiki/Main/TestEditV1.md" when the
		// actual topic (e.g. one created via cmd/wiki-vim, which names
		// topics with no extension at all) is just "TestEditV1".
		// topicName already treats leading path segments as cosmetic
		// (see its own doc comment); this extends the same leniency to
		// one trailing extension, retried only after the exact name
		// fails, so a topic whose real name happens to contain a dot
		// still resolves on the first, literal attempt.
		if stripped := strings.TrimSuffix(name, filepath.Ext(name)); stripped != "" && stripped != name {
			if src2, err2 := in.c.wiki.GetTopicSource(ctx, &wikiv1.GetTopicRequest{Web: in.web, Topic: stripped}); err2 == nil {
				return &filesystem.FileContent{Content: sliceLines(src2.GetBody(), req.Offset, req.Limit)}, nil
			}
		}
		return nil, fmt.Errorf("read %s: %w", req.FilePath, err)
	}
	return &filesystem.FileContent{Content: sliceLines(src.GetBody(), req.Offset, req.Limit)}, nil
}

// matchesGlobAndType reports whether a topic (name, and its path
// relative to whatever base a search started from) passes a GrepRequest/
// GlobInfoRequest's optional Glob/FileType filters - mirroring eino's
// own InMemoryBackend: a Glob containing "/" or "**" matches against the
// relative path, otherwise against the bare name. FileType is matched
// against the name's extension directly (not eino's large language-alias
// table), since wiki topic names essentially never have one - it's kept
// for interface completeness rather than because it does useful work
// here.
func matchesGlobAndType(name, relPath, glob, fileType string) bool {
	if glob != "" {
		matchPath := name
		if strings.Contains(glob, "/") || strings.Contains(glob, "**") {
			matchPath = relPath
		}
		ok, err := doublestar.Match(glob, matchPath)
		if err != nil || !ok {
			return false
		}
	}
	if fileType != "" {
		ext := strings.TrimPrefix(filepath.Ext(name), ".")
		if ext != fileType {
			return false
		}
	}
	return true
}

// Search for matching content in files
func (in *internalFS) GrepRaw(ctx context.Context, req *filesystem.GrepRequest) ([]filesystem.GrepMatch, error) {
	if req.Pattern == "" {
		return nil, fmt.Errorf("pattern cannot be empty")
	}
	pattern := req.Pattern
	if req.CaseInsensitive {
		pattern = "(?i)" + pattern
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("invalid regex pattern: %w", err)
	}

	byName, children, err := in.hierarchy(ctx)
	if err != nil {
		return nil, err
	}

	base, err := resolveBaseTopic(req.Path, byName)
	if err != nil {
		return nil, err
	}

	scope := make(map[string]bool)
	for _, name := range descendants(base, children) {
		if matchesGlobAndType(name, relativePath(name, base, byName), req.Glob, req.FileType) {
			scope[name] = true
		}
	}

	bodies, err := in.grepCandidateBodies(ctx, req.Pattern, scope)
	if err != nil {
		return nil, err
	}

	var matches []filesystem.GrepMatch
	for name, body := range bodies {
		matches = append(matches, grepTopic(relativePath(name, base, byName), body, re, req)...)
	}
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].Path != matches[j].Path {
			return matches[i].Path < matches[j].Path
		}
		return matches[i].Line < matches[j].Line
	})
	return matches, nil
}

// grepCandidateBodies returns body text for every topic name in scope -
// the set GrepRaw actually needs to search. When pattern is a plain
// literal (no regex metacharacters - regexp.QuoteMeta(pattern) ==
// pattern) at least 3 characters long (topic_search's trigram index's
// own minimum - see ftsMinQueryLen in wiki/search_content.go), it's used
// as a WikiService.SearchContent pre-filter first: that RPC is backed by
// topic_search (db/schema.sql's FTS5 trigram index), matched as an exact,
// case-insensitive substring - the same thing grep's own literal
// substring matching means, verified in this package's own tests
// against a document containing every trigram of a query scattered
// non-contiguously (which must NOT match). Because the trigram index is
// case-insensitive while a literal grep pattern defaults to case-
// sensitive, SearchContent can only return a *superset* of what should
// actually match - never a subset - so grepTopic's own regex re-check
// below is still what decides what's a real match; this only decides
// which topics' bodies are worth fetching at all. A regex pattern (real
// metacharacters) or a query shorter than 3 characters can't be
// pre-filtered this way and falls back to fetching every scoped topic's
// body directly via GetTopicSource, same as before this existed.
func (in *internalFS) grepCandidateBodies(ctx context.Context, pattern string, scope map[string]bool) (map[string]string, error) {
	if len(scope) == 0 {
		return nil, nil
	}

	if regexp.QuoteMeta(pattern) == pattern && len(pattern) >= 3 {
		resp, err := in.c.wiki.SearchContent(ctx, &wikiv1.SearchContentRequest{Web: in.web, Query: pattern})
		if err != nil {
			return nil, fmt.Errorf("search content: %w", err)
		}
		bodies := make(map[string]string, len(resp.GetMatches()))
		for _, m := range resp.GetMatches() {
			if scope[m.GetTopic()] {
				bodies[m.GetTopic()] = m.GetBody()
			}
		}
		return bodies, nil
	}

	bodies := make(map[string]string, len(scope))
	for name := range scope {
		src, err := in.c.wiki.GetTopicSource(ctx, &wikiv1.GetTopicRequest{Web: in.web, Topic: name})
		if err != nil {
			return nil, fmt.Errorf("get topic source %s/%s: %w", in.web, name, err)
		}
		bodies[name] = src.GetBody()
	}
	return bodies, nil
}

// grepTopic finds every regex match in content and returns it as
// GrepMatch entries at path, honoring EnableMultiline/BeforeLines/
// AfterLines the same way eino's own InMemoryBackend.GrepRaw does
// (adk/filesystem/backend_inmemory.go): single-line matching by
// default, full-content regex matching mapped back to line numbers when
// EnableMultiline is set.
func grepTopic(path, content string, re *regexp.Regexp, req *filesystem.GrepRequest) []filesystem.GrepMatch {
	lines := strings.Split(content, "\n")
	matchedLines := map[int]bool{}

	if req.EnableMultiline {
		for _, m := range re.FindAllStringIndex(content, -1) {
			start := 1 + strings.Count(content[:m[0]], "\n")
			end := 1 + strings.Count(content[:m[1]], "\n")
			for ln := start; ln <= end && ln <= len(lines); ln++ {
				matchedLines[ln] = true
			}
		}
	} else {
		for i, line := range lines {
			if re.MatchString(line) {
				matchedLines[i+1] = true
			}
		}
	}
	if len(matchedLines) == 0 {
		return nil
	}

	include := matchedLines
	if req.BeforeLines > 0 || req.AfterLines > 0 {
		include = map[int]bool{}
		for ln := range matchedLines {
			start := ln - req.BeforeLines
			if start < 1 {
				start = 1
			}
			end := ln + req.AfterLines
			if end > len(lines) {
				end = len(lines)
			}
			for l := start; l <= end; l++ {
				include[l] = true
			}
		}
	}

	result := make([]filesystem.GrepMatch, 0, len(include))
	for ln := range include {
		result = append(result, filesystem.GrepMatch{Path: path, Line: ln, Content: lines[ln-1]})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Line < result[j].Line })
	return result
}

// Match files by glob pattern
func (in *internalFS) GlobInfo(ctx context.Context, req *filesystem.GlobInfoRequest) ([]filesystem.FileInfo, error) {
	byName, children, err := in.hierarchy(ctx)
	if err != nil {
		return nil, err
	}

	base, err := resolveBaseTopic(req.Path, byName)
	if err != nil {
		return nil, err
	}

	isAbsolute := strings.HasPrefix(req.Pattern, "/")
	pattern := strings.TrimPrefix(req.Pattern, "/")

	var result []filesystem.FileInfo
	for _, name := range descendants(base, children) {
		node := byName[name]

		var matchPath, resultPath string
		if isAbsolute {
			resultPath = fullPath(name, byName)
			matchPath = strings.TrimPrefix(resultPath, "/")
		} else {
			matchPath = relativePath(name, base, byName)
			resultPath = matchPath
		}

		matched, err := doublestar.Match(pattern, matchPath)
		if err != nil {
			return nil, fmt.Errorf("invalid glob pattern: %w", err)
		}
		if !matched {
			continue
		}
		result = append(result, filesystem.FileInfo{
			Path:       resultPath,
			IsDir:      len(children[name]) > 0,
			Size:       node.size,
			ModifiedAt: formatModTime(node.timestamp),
		})
	}
	return result, nil
}

// Write creates or overwrites a topic's content. The topic addressed is
// path's last segment (see topicName); if the topic doesn't exist yet,
// it's created with path's second-to-last segment as its TOPICPARENT
// (see parentName), so writing to a new nested path both creates the
// topic and places it in the hierarchy that path describes. If the
// topic already exists, its TOPICPARENT is updated to match path too -
// so writing to a new path reparents it, same as a real filesystem's
// Write to a new location would move it - but its existing form/fields/
// preferences are preserved unchanged, since Write only means to
// replace content, not strip a topic's other metadata.
func (in *internalFS) Write(ctx context.Context, req *filesystem.WriteRequest) error {
	name := topicName(req.FilePath)
	if name == "" {
		return fmt.Errorf("not a file: %s", req.FilePath)
	}

	putReq := &wikiv1.PutTopicRequest{
		Web:         in.web,
		Topic:       name,
		Body:        req.Content,
		ParentTopic: parentName(req.FilePath),
	}
	if src, err := in.c.wiki.GetTopicSource(ctx, &wikiv1.GetTopicRequest{Web: in.web, Topic: name}); err == nil {
		putReq.FormName = src.GetFormName()
		putReq.Fields = src.GetFields()
		putReq.Preferences = src.GetPreferences()
	}

	if _, err := in.c.wiki.PutTopic(ctx, putReq); err != nil {
		return fmt.Errorf("write %s: %w", req.FilePath, err)
	}
	return nil
}

// Edit file content (string replacement)
//
// Edit replaces OldString with NewString in a topic's raw body, leaving
// its TOPICPARENT/form/fields/preferences untouched - unlike Write,
// Edit never reparents a topic, since its path is only used to find the
// topic to modify, not to describe where it should live.
func (in *internalFS) Edit(ctx context.Context, req *filesystem.EditRequest) error {
	name := topicName(req.FilePath)
	if name == "" {
		return fmt.Errorf("not a file: %s", req.FilePath)
	}
	if req.OldString == "" {
		return fmt.Errorf("oldString must be non-empty")
	}

	src, err := in.c.wiki.GetTopicSource(ctx, &wikiv1.GetTopicRequest{Web: in.web, Topic: name})
	if err != nil {
		return fmt.Errorf("edit %s: %w", req.FilePath, err)
	}

	content := src.GetBody()
	if !strings.Contains(content, req.OldString) {
		return fmt.Errorf("oldString not found in file: %s", req.FilePath)
	}
	if !req.ReplaceAll {
		first := strings.Index(content, req.OldString)
		if strings.Contains(content[first+len(req.OldString):], req.OldString) {
			return fmt.Errorf("multiple occurrences of oldString found in file %s, but ReplaceAll is false", req.FilePath)
		}
	}

	var newContent string
	if req.ReplaceAll {
		newContent = strings.ReplaceAll(content, req.OldString, req.NewString)
	} else {
		newContent = strings.Replace(content, req.OldString, req.NewString, 1)
	}

	_, err = in.c.wiki.PutTopic(ctx, &wikiv1.PutTopicRequest{
		Web:         in.web,
		Topic:       name,
		Body:        newContent,
		ParentTopic: src.GetParentTopic(),
		FormName:    src.GetFormName(),
		Fields:      src.GetFields(),
		Preferences: src.GetPreferences(),
	})
	if err != nil {
		return fmt.Errorf("edit %s: %w", req.FilePath, err)
	}
	return nil
}
