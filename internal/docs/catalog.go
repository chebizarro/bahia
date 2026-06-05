package docs

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const DefaultBasePath = "docs/user-guide"

var (
	ErrNotFound        = errors.New("documentation topic not found")
	ErrInvalidTopic    = errors.New("invalid documentation topic")
	ErrOutsideDocsRoot = errors.New("documentation link points outside docs root")
	ErrUnsupportedLink = errors.New("documentation link is not a markdown document")
)

type Service struct {
	basePath string
}

type Topic struct {
	Topic      string `json:"topic"`
	Title      string `json:"title"`
	Category   string `json:"category"`
	SourcePath string `json:"sourcePath"`
	Href       string `json:"href"`
}

type Document struct {
	Topic    Topic          `json:"topic"`
	Markdown string         `json:"markdown"`
	Links    []DocumentLink `json:"links,omitempty"`
}

type DocumentLink struct {
	Original string `json:"original"`
	Href     string `json:"href,omitempty"`
	Topic    string `json:"topic,omitempty"`
	External bool   `json:"external,omitempty"`
	Status   string `json:"status"`
	Error    string `json:"error,omitempty"`
}

type LinkResolution struct {
	Original string `json:"original"`
	Href     string `json:"href"`
	Topic    string `json:"topic,omitempty"`
	External bool   `json:"external,omitempty"`
}

var markdownLinkPattern = regexp.MustCompile(`!?\[[^\]\n]+\]\(([^)\s]+)(?:\s+['"][^)]*['"])?\)`)

func New(basePath string) Service {
	if strings.TrimSpace(basePath) == "" {
		basePath = DefaultBasePath
	}
	return Service{basePath: filepath.Clean(basePath)}
}

func (s Service) BasePath() string {
	if s.basePath == "" {
		return DefaultBasePath
	}
	return s.basePath
}

func (s Service) Catalog(ctx context.Context) ([]Topic, error) {
	basePath := s.BasePath()
	info, err := os.Stat(basePath)
	if errors.Is(err, os.ErrNotExist) {
		return []Topic{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("stat documentation root: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("documentation root is not a directory: %s", basePath)
	}

	seen := map[string]string{}
	topics := []Topic{}
	err = filepath.WalkDir(basePath, func(filePath string, d os.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() || !strings.EqualFold(filepath.Ext(filePath), ".md") {
			return nil
		}

		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}

		relPath, err := filepath.Rel(basePath, filePath)
		if err != nil {
			return fmt.Errorf("resolve documentation path: %w", err)
		}
		relPath = filepath.ToSlash(relPath)
		topic := TopicFromPath(relPath)
		if previous, ok := seen[topic]; ok {
			return fmt.Errorf("duplicate documentation topic %q for %s and %s", topic, previous, relPath)
		}
		seen[topic] = relPath

		content, err := s.readFile(relPath)
		if err != nil {
			return fmt.Errorf("read documentation metadata %s: %w", relPath, err)
		}
		topics = append(topics, Topic{
			Topic:      topic,
			Title:      firstHeading(content),
			Category:   categoryForPath(relPath),
			SourcePath: relPath,
			Href:       "/docs/" + topic,
		})
		return nil
	})
	if err != nil {
		return nil, err
	}

	sort.Slice(topics, func(i, j int) bool {
		if topics[i].Topic != topics[j].Topic {
			return topics[i].Topic < topics[j].Topic
		}
		return topics[i].SourcePath < topics[j].SourcePath
	})
	return topics, nil
}

func (s Service) Read(ctx context.Context, topic string) (Document, error) {
	return s.read(ctx, topic, false)
}

func (s Service) ReadWithLinks(ctx context.Context, topic string) (Document, error) {
	return s.read(ctx, topic, true)
}

func (s Service) read(ctx context.Context, topic string, includeLinks bool) (Document, error) {
	topic = strings.TrimSpace(topic)
	if unsafeTopic(topic) {
		return Document{}, ErrInvalidTopic
	}

	catalog, err := s.Catalog(ctx)
	if err != nil {
		return Document{}, err
	}
	for _, item := range catalog {
		if item.Topic != topic {
			continue
		}
		content, err := s.readFile(item.SourcePath)
		if err != nil {
			return Document{}, fmt.Errorf("read documentation topic %s: %w", topic, err)
		}
		document := Document{Topic: item, Markdown: string(content)}
		if includeLinks {
			document.Links = s.resolveDocumentLinksWithCatalog(item.SourcePath, document.Markdown, catalog)
		}
		return document, nil
	}
	return Document{}, ErrNotFound
}

func (s Service) ResolveDocumentLinks(ctx context.Context, sourcePath string, markdown string) []DocumentLink {
	catalog, err := s.Catalog(ctx)
	if err != nil {
		return []DocumentLink{{Status: "error", Error: err.Error()}}
	}
	return s.resolveDocumentLinksWithCatalog(sourcePath, markdown, catalog)
}

func (s Service) resolveDocumentLinksWithCatalog(sourcePath string, markdown string, catalog []Topic) []DocumentLink {
	seen := map[string]bool{}
	links := []DocumentLink{}
	for _, match := range markdownLinkPattern.FindAllStringSubmatch(markdown, -1) {
		if len(match) < 2 {
			continue
		}
		rawHref := strings.TrimSpace(match[1])
		if rawHref == "" || seen[rawHref] {
			continue
		}
		seen[rawHref] = true
		resolved, err := s.resolveMarkdownLinkWithCatalog(sourcePath, rawHref, catalog)
		if err != nil {
			links = append(links, DocumentLink{Original: rawHref, Status: linkErrorStatus(err), Error: err.Error()})
			continue
		}
		links = append(links, DocumentLink{
			Original: resolved.Original,
			Href:     resolved.Href,
			Topic:    resolved.Topic,
			External: resolved.External,
			Status:   "resolved",
		})
	}
	return links
}

func (s Service) ResolveMarkdownLink(ctx context.Context, sourcePath string, rawHref string) (LinkResolution, error) {
	catalog, err := s.Catalog(ctx)
	if err != nil {
		return LinkResolution{}, err
	}
	return s.resolveMarkdownLinkWithCatalog(sourcePath, rawHref, catalog)
}

func (s Service) resolveMarkdownLinkWithCatalog(sourcePath string, rawHref string, catalog []Topic) (LinkResolution, error) {
	if strings.TrimSpace(rawHref) == "" {
		return LinkResolution{}, ErrUnsupportedLink
	}

	parsed, err := url.Parse(rawHref)
	if err != nil {
		return LinkResolution{}, fmt.Errorf("parse documentation link: %w", err)
	}
	if parsed.IsAbs() || parsed.Host != "" || strings.HasPrefix(rawHref, "//") {
		if !allowedExternalScheme(parsed.Scheme) {
			return LinkResolution{}, ErrUnsupportedLink
		}
		return LinkResolution{Original: rawHref, Href: rawHref, External: true}, nil
	}

	sourceRel, err := s.relativePath(sourcePath)
	if err != nil {
		return LinkResolution{}, err
	}

	targetRel := sourceRel
	if parsed.Path != "" {
		if strings.HasPrefix(parsed.Path, "/") {
			return LinkResolution{}, ErrOutsideDocsRoot
		}
		candidate := path.Clean(path.Join(path.Dir(sourceRel), parsed.Path))
		if candidate == "." || candidate == ".." || strings.HasPrefix(candidate, "../") {
			return LinkResolution{}, ErrOutsideDocsRoot
		}
		targetRel = candidate
	}
	if !strings.EqualFold(path.Ext(targetRel), ".md") {
		return LinkResolution{}, ErrUnsupportedLink
	}

	topic := TopicFromPath(targetRel)
	if unsafeTopic(topic) {
		return LinkResolution{}, ErrInvalidTopic
	}
	found := false
	for _, item := range catalog {
		if item.Topic == topic && item.SourcePath == targetRel {
			found = true
			break
		}
	}
	if !found {
		return LinkResolution{}, ErrNotFound
	}

	href := "/docs/" + topic
	if parsed.Fragment != "" {
		href += "#" + parsed.EscapedFragment()
	}
	return LinkResolution{Original: rawHref, Href: href, Topic: topic}, nil
}

func TopicFromPath(relPath string) string {
	relPath = filepath.ToSlash(relPath)
	relPath = strings.TrimSuffix(relPath, path.Ext(relPath))
	parts := strings.Split(relPath, "/")
	for i, part := range parts {
		parts[i] = strings.TrimSpace(part)
	}
	return strings.Join(parts, "-")
}

func unsafeTopic(topic string) bool {
	if topic == "" || topic == "." || topic == ".." {
		return true
	}
	return strings.Contains(topic, "/") || strings.Contains(topic, "\\") || strings.Contains(topic, "..")
}

func firstHeading(content []byte) string {
	scanner := bufio.NewScanner(strings.NewReader(string(content)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "# ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "# "))
		}
	}
	return ""
}

func allowedExternalScheme(scheme string) bool {
	switch strings.ToLower(scheme) {
	case "", "http", "https", "mailto":
		return true
	default:
		return false
	}
}

func linkErrorStatus(err error) string {
	switch {
	case errors.Is(err, ErrNotFound):
		return "not_found"
	case errors.Is(err, ErrOutsideDocsRoot):
		return "outside_docs_root"
	case errors.Is(err, ErrUnsupportedLink):
		return "unsupported"
	case errors.Is(err, ErrInvalidTopic):
		return "invalid_topic"
	default:
		return "error"
	}
}

func categoryForPath(relPath string) string {
	if strings.HasPrefix(relPath, "features/") {
		return "feature"
	}
	switch path.Base(relPath) {
	case "cli-reference.md", "mcp-tools.md", "nostr-integration.md":
		return "reference"
	default:
		return "guide"
	}
}

func (s Service) readFile(relPath string) ([]byte, error) {
	safePath, err := s.safePath(relPath)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(safePath)
}

func (s Service) safePath(relPath string) (string, error) {
	relPath, err := s.relativePath(relPath)
	if err != nil {
		return "", err
	}

	baseResolved, err := filepath.EvalSymlinks(s.BasePath())
	if err != nil {
		return "", fmt.Errorf("resolve documentation root symlinks: %w", err)
	}
	targetLexical := filepath.Join(baseResolved, filepath.FromSlash(relPath))
	targetResolved, err := filepath.EvalSymlinks(targetLexical)
	if err != nil {
		return "", err
	}
	inside, err := pathInside(baseResolved, targetResolved)
	if err != nil {
		return "", err
	}
	if !inside {
		return "", ErrOutsideDocsRoot
	}
	return targetResolved, nil
}

func pathInside(root string, candidate string) (bool, error) {
	rel, err := filepath.Rel(root, candidate)
	if err != nil {
		return false, err
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))), nil
}

func (s Service) relativePath(candidate string) (string, error) {
	candidate = strings.TrimSpace(candidate)
	if candidate == "" {
		return "", ErrOutsideDocsRoot
	}

	var rel string
	if filepath.IsAbs(candidate) {
		baseAbs, err := filepath.Abs(s.BasePath())
		if err != nil {
			return "", fmt.Errorf("resolve documentation root: %w", err)
		}
		candidateAbs, err := filepath.Abs(candidate)
		if err != nil {
			return "", fmt.Errorf("resolve documentation source path: %w", err)
		}
		relPath, err := filepath.Rel(baseAbs, candidateAbs)
		if err != nil {
			return "", fmt.Errorf("resolve documentation source path: %w", err)
		}
		rel = relPath
	} else {
		rel = candidate
	}

	rel = filepath.ToSlash(filepath.Clean(rel))
	if rel == "." || rel == ".." || strings.HasPrefix(rel, "../") || strings.HasPrefix(rel, "/") {
		return "", ErrOutsideDocsRoot
	}
	return rel, nil
}
