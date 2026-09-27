package main

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/fsnotify/fsnotify"
)

// SnapshotStore holds an in-memory map of the watched filesystem tree,
// kept live by fsnotify, and handed out as a []FileEntry only when the
// executor asks for it (i.e. only when the task actually needs context —
// nothing is sent anywhere just because it changed).
type SnapshotStore struct {
	mu      sync.RWMutex
	entries map[string]FileEntry // keyed by path
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

// Snapshot returns a point-in-time copy suitable for JSON-serializing into
// an Action's FileSystemPayload, SORTED NEWEST-FIRST by modification time.
//
// The ordering matters a lot: the underlying store is a map, which Go
// iterates in random order, so without this the planner would receive the
// files as an unordered jumble and could not answer "open the most recent
// screenshot" — it would just grab the first file that looked like a match.
// Newest-first means the freshly-created file the user is almost certainly
// asking about sits right at the top of the list.
func (s *SnapshotStore) Snapshot() []FileEntry {
	s.mu.RLock()
	out := make([]FileEntry, 0, len(s.entries))
	for _, e := range s.entries {
		out = append(out, e)
	}
	s.mu.RUnlock()

	sort.Slice(out, func(i, j int) bool {
		return out[i].ModTime > out[j].ModTime // most recently modified first
	})
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

	// fsnotify doesn't watch recursively on its own — we add every
	// directory we find while building the initial snapshot.
	for _, root := range roots {
		_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return nil // skip unreadable entries, don't abort the walk
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
			if info, err := os.Stat(event.Name); err == nil {
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
