package main

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"unicode"

	"github.com/fsnotify/fsnotify"
)

// Limits for what goes to the planner. The full tree can be tens of
// thousands of entries (one project folder is enough), which used to cost
// hundreds of thousands of tokens per planning call. The planner only needs
// the handful of entries relevant to the task, plus enough of the newest
// files and the top-level folders to orient itself.
const (
	snapshotMaxEntries  = 150 // hard cap on entries sent per snapshot
	snapshotMaxMatches  = 100 // entries whose path matches words from the task
	snapshotMaxTopLevel = 50  // top-level folders of each root, always included
)

// skippedDirNames are never walked or watched: build output, dependency
// caches and VCS internals are huge, change constantly, and are never what
// a user means by "my files".
var skippedDirNames = map[string]bool{
	"node_modules": true, ".git": true, ".venv": true, "venv": true,
	"__pycache__": true, ".next": true, "dist": true, "build": true,
	"target": true, ".cache": true,
}

// skipDir reports whether a directory with this base name is excluded from
// the walk and the watch (the list above, plus any dot-directory).
func skipDir(name string) bool {
	return skippedDirNames[strings.ToLower(name)] || strings.HasPrefix(name, ".")
}

// insideSkippedDir reports whether path lies under an excluded directory
// (relative to its watch root), so fsnotify events for such paths — which
// can still arrive for a skipped dir's own create/rename — are ignored.
func insideSkippedDir(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil || strings.HasPrefix(rel, "..") {
		return false
	}
	parts := strings.Split(rel, string(filepath.Separator))
	for _, p := range parts[:len(parts)-1] {
		if skipDir(p) {
			return true
		}
	}
	return false
}

// SnapshotStore holds an in-memory map of the watched filesystem tree,
// kept live by fsnotify, and handed out as a []FileEntry only when the
// executor asks for it (i.e. only when the task actually needs context —
// nothing is sent anywhere just because it changed).
type SnapshotStore struct {
	mu      sync.RWMutex
	entries map[string]FileEntry // keyed by path
	roots   []string             // the watch roots, for "top-level folder" detection
}

func NewSnapshotStore() *SnapshotStore {
	return &SnapshotStore{entries: make(map[string]FileEntry)}
}

func (s *SnapshotStore) upsert(path string, info os.FileInfo) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := FileTypeFile
	if info.IsDir() {
		t = FileTypeDir
	}
	s.entries[path] = FileEntry{
		Path:    path,
		Type:    t,
		Size:    uint64(info.Size()),
		ModTime: info.ModTime().Unix(),
	}
}

func (s *SnapshotStore) remove(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.entries, path)
}

// Snapshot returns a filtered, point-in-time selection of the tree for the
// planner, SORTED NEWEST-FIRST by modification time, with the home
// directory in every path replaced by "~" (shorter, and it keeps the
// Windows user name out of the prompt).
//
// At most snapshotMaxEntries entries are returned, chosen in this order:
//  1. the watch roots and their top-level folders (always, so the planner
//     knows where things can live),
//  2. up to snapshotMaxMatches entries whose path contains a word from
//     query (the task description, plus any replan reason/answers) —
//     case-insensitive, words of 3+ characters, stop-words ignored,
//  3. then the newest remaining entries by modification time.
//
// The ordering matters a lot: the underlying store is a map, which Go
// iterates in random order, so without sorting the planner would receive
// the files as an unordered jumble and could not answer "open the most
// recent screenshot". Newest-first means the freshly-created file the user
// is almost certainly asking about sits right at the top of the list.
func (s *SnapshotStore) Snapshot(query string) []FileEntry {
	s.mu.RLock()
	all := make([]FileEntry, 0, len(s.entries))
	for _, e := range s.entries {
		all = append(all, e)
	}
	roots := append([]string(nil), s.roots...)
	s.mu.RUnlock()

	sort.Slice(all, func(i, j int) bool {
		return all[i].ModTime > all[j].ModTime // most recently modified first
	})

	isRoot := make(map[string]bool, len(roots))
	for _, r := range roots {
		isRoot[strings.ToLower(filepath.Clean(r))] = true
	}

	picked := make(map[string]bool)
	out := make([]FileEntry, 0, snapshotMaxEntries)
	take := func(e FileEntry) {
		if len(out) < snapshotMaxEntries && !picked[e.Path] {
			picked[e.Path] = true
			out = append(out, e)
		}
	}

	// 1. Roots and their top-level folders.
	top := 0
	for _, e := range all {
		if e.Type != FileTypeDir {
			continue
		}
		p := strings.ToLower(filepath.Clean(e.Path))
		if isRoot[p] || (isRoot[strings.ToLower(filepath.Dir(e.Path))] && top < snapshotMaxTopLevel) {
			if !isRoot[p] {
				top++
			}
			take(e)
		}
	}

	// 2. Entries matching words from the task, most matched words first,
	// newest first within a tie (the sort is stable over the newest-first
	// order above).
	if words := queryWords(query); len(words) > 0 {
		type scored struct {
			e     FileEntry
			score int
		}
		var matches []scored
		for _, e := range all {
			lp := strings.ToLower(e.Path)
			n := 0
			for _, w := range words {
				if strings.Contains(lp, w) {
					n++
				}
			}
			if n > 0 {
				matches = append(matches, scored{e, n})
			}
		}
		sort.SliceStable(matches, func(i, j int) bool { return matches[i].score > matches[j].score })
		for i := 0; i < len(matches) && i < snapshotMaxMatches; i++ {
			take(matches[i].e)
		}
	}

	// 3. Fill up with the newest entries.
	for _, e := range all {
		if len(out) >= snapshotMaxEntries {
			break
		}
		take(e)
	}

	sort.SliceStable(out, func(i, j int) bool { return out[i].ModTime > out[j].ModTime })

	if home, err := os.UserHomeDir(); err == nil && home != "" {
		for i := range out {
			if rel, err := filepath.Rel(home, out[i].Path); err == nil && !strings.HasPrefix(rel, "..") {
				out[i].Path = "~" + string(filepath.Separator) + rel
			}
		}
	}
	return out
}

// stopWords are common words that would match half the tree ("the",
// "file", "folder") without saying anything about which entry is meant.
var stopWords = map[string]bool{
	"the": true, "and": true, "for": true, "from": true, "with": true, "into": true,
	"that": true, "this": true, "then": true, "than": true, "them": true, "they": true,
	"open": true, "file": true, "files": true, "folder": true, "folders": true,
	"my": true, "your": true, "you": true, "please": true, "can": true, "could": true,
	"want": true, "need": true, "put": true, "get": true, "make": true, "send": true,
	"all": true, "any": true, "some": true, "what": true, "which": true, "when": true,
	"where": true, "last": true, "latest": true, "recent": true, "most": true,
	"new": true, "newest": true, "one": true, "out": true, "use": true, "using": true,
	"find": true, "show": true, "take": true, "took": true, "just": true, "have": true,
	"has": true, "was": true, "are": true, "not": true, "its": true, "it's": true,
	"about": true, "there": true, "here": true, "also": true, "user": true,
	"answered": true,
}

// queryWords splits text into lowercase words of 3+ characters, dropping
// stop-words and duplicates.
func queryWords(text string) []string {
	fields := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	seen := make(map[string]bool)
	var out []string
	for _, f := range fields {
		if len([]rune(f)) < 3 || stopWords[f] || seen[f] {
			continue
		}
		seen[f] = true
		out = append(out, f)
	}
	return out
}

// defaultWatchRoots scopes the watcher to the common user folders rather
// than the entire drive — walking/watching an entire disk is slow and can
// hit OS watch-handle limits. Widen this list (or point it at the whole
// home directory) if your use case needs more.
func defaultWatchRoots() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	candidates := []string{"Desktop", "Documents", "Downloads", "Pictures"}
	var roots []string
	for _, c := range candidates {
		p := filepath.Join(home, c)
		if info, err := os.Stat(p); err == nil && info.IsDir() {
			roots = append(roots, p)
		}
	}
	return roots
}

// RunFSWatcher builds the initial tree, then keeps it updated in real time
// until ctx is cancelled. Safe to run as its own goroutine.
func RunFSWatcher(ctx context.Context, roots []string, store *SnapshotStore) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Printf("fswatcher: failed to create watcher: %v", err)
		return
	}
	defer watcher.Close()

	store.mu.Lock()
	store.roots = append([]string(nil), roots...)
	store.mu.Unlock()

	// rootOf finds which watch root a path is under, for insideSkippedDir.
	rootOf := func(path string) string {
		for _, r := range roots {
			if rel, err := filepath.Rel(r, path); err == nil && !strings.HasPrefix(rel, "..") {
				return r
			}
		}
		return ""
	}

	// fsnotify doesn't watch recursively on its own — we add every
	// directory we find while building the initial snapshot. Excluded
	// directories (node_modules, .git, ...) are skipped entirely: neither
	// they nor anything inside them is stored or watched.
	for _, root := range roots {
		_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return nil // skip unreadable entries, don't abort the walk
			}
			if info.IsDir() && path != root && skipDir(info.Name()) {
				return filepath.SkipDir
			}
			store.upsert(path, info)
			if info.IsDir() {
				if werr := watcher.Add(path); werr != nil {
					log.Printf("fswatcher: watch add failed for %s: %v", path, werr)
				}
			}
			return nil
		})
	}

	for {
		select {
		case <-ctx.Done():
			return

		case event, ok := <-watcher.Events:
			if !ok {
				return
			}
			// Stat-based, not op-based: whatever the event says, the ground
			// truth is whether the path exists NOW. If it does, it was
			// created/written/renamed-into-place → upsert; if it doesn't, it
			// was removed/renamed-away → remove. This fixes a real bug where
			// a Rename event (fired when a file is renamed INTO this name —
			// exactly how some screenshot tools save, temp file then rename)
			// was treated as a delete, dropping the brand-new file the user
			// is most likely about to ask about.
			if r := rootOf(event.Name); r != "" && insideSkippedDir(r, event.Name) {
				continue
			}
			if info, err := os.Stat(event.Name); err == nil {
				if info.IsDir() && skipDir(info.Name()) {
					continue // a new node_modules/.git/... — never track it
				}
				store.upsert(event.Name, info)
				if info.IsDir() && event.Op&fsnotify.Create != 0 {
					_ = watcher.Add(event.Name) // watch newly created dirs
				}
			} else {
				store.remove(event.Name)
			}

		case err, ok := <-watcher.Errors:
			if !ok {
				return
			}
			log.Printf("fswatcher: %v", err)
		}
	}
}
