module remoteworker

go 1.22

require (
	github.com/coder/websocket v1.8.12
	github.com/fsnotify/fsnotify v1.7.0
	github.com/skip2/go-qrcode v0.0.0-20200617195104-da1b6568686e
	golang.org/x/sys v0.21.0
)

// golang.org/x/sys is normally fetched through the golang.org redirector,
// which some sandboxed/offline build environments can't reach. The GitHub
// mirror is the same module content under the same canonical import path,
// so this is a safe, common workaround — remove it if your environment can
// reach golang.org directly.
replace golang.org/x/sys => github.com/golang/sys v0.21.0
