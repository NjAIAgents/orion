package web

// The seam between the Go binary and the front end (OR-64).
//
// `orion web` ships as one binary with nothing to install beside it, so the
// page is compiled in: //go:embed reads static/ at build time and the result
// is served out of memory. That is the whole mechanism, and it has one
// failure mode worth designing around.
//
// //go:embed IS A COMPILE-TIME PATTERN, AND A PATTERN THAT MATCHES NOTHING IS
// A BUILD ERROR -- not an empty directory, not a warning. Embedding built
// output directly (`//go:embed static/*.js`) would therefore make
// `go build ./...` fail on every machine that had not run the front-end build
// first: CI, a packaging job, anyone who only wants the CLI. The front end
// would have become a build dependency of the binary rather than an input to
// it.
//
// So static/index.html is COMMITTED, and the pattern names the directory
// rather than what the directory is expected to contain. With no front-end
// build ever run, "/" serves that placeholder; once OR-51's build writes its
// output into static/, the same pattern picks it up and "/" serves the real
// page. Deleting the placeholder as cleanup after OR-51 lands restores
// exactly the failure this file exists to prevent, which is why a test holds
// it in place.
//
// The directory is static/ and not dist/ for a duller reason: .gitignore
// ignores dist/ everywhere, so a build output directory by that name would
// never have been committed, and the missing placeholder would have shown up
// as a broken build on a fresh clone rather than on the machine that wrote
// it.

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed static
var assets embed.FS

// The vendored runtime (OR-66) lives outside static/, in the directory its
// own VENDOR.md checksums -- a page needing it is one bare `import` away
// from a wrong file, and moving preact.module.js and htm.module.js under
// static/ would mean two copies to keep byte-identical to that checksum, or
// one broken by a future edit made in the wrong tree. Embedded and served
// from its own route instead, so the pin OR-66 committed to stays the only
// copy that exists.
//
//go:embed ui/vendor
var vendored embed.FS

// The front end answers on "/", registered the way every other page will be
// (server.go): from an init in the file that owns the handler, so nothing
// has to edit a shared list. Without this, `orion web` (OR-61) prints an
// address whose root is a 404.
func init() {
	HandleReadOnly("/", Assets())
	HandleReadOnly("/vendor/", Vendored())
}

// Assets is the front end, ready to mount: the server skeleton gives it "/",
// and a browser asking for the root gets static/index.html.
//
// http.FileServer does the serving -- content type, ETag, Range, HEAD and
// conditional requests are all its work, and none of that is worth
// reimplementing. It is wrapped rather than returned directly because two of
// its defaults are wrong for a read-only tree compiled into a binary:
//
//   - It answers every method. A POST or DELETE to "/" is served exactly like
//     a GET, which tells a client that a write reached something. Nothing here
//     is writable, so anything but GET or HEAD is 405 with an Allow header.
//   - It canonicalises any path ending in "/index.html" to its directory with
//     a 301 BEFORE it checks whether the file exists. So a request for
//     /static/index.html -- the embedded path, which fs.Sub has already
//     stripped out of the URL space -- redirected instead of 404ing, and the
//     redirect implied a static/ directory that is not there. Missing is
//     missing: the existence check happens first here, and only a path that
//     resolves is handed on.
//
// Vendored serves the runtime OR-66 committed -- preact.module.js and
// htm.module.js -- at /vendor/, exactly as VENDOR.md's own import example
// names them: `./vendor/preact.module.js` resolves correctly from a page
// served at "/".
//
// No method guard and no existence pre-check the way Assets has both: this
// tree holds exactly two files, both named in VENDOR.md, and
// http.FileServer's own 404 is already the right answer for anything else
// requested under this prefix.
func Vendored() http.Handler {
	sub, err := fs.Sub(vendored, "ui/vendor")
	if err != nil {
		panic("web: embedded ui/vendor is unreachable: " + err.Error())
	}
	return http.StripPrefix("/vendor/", http.FileServer(http.FS(sub)))
}

func Assets() http.Handler {
	// Strip the static/ prefix, so a request for "/" resolves to index.html
	// rather than needing "/static/index.html". fs.Sub fails only on a
	// malformed path, and this one is a compile-time constant that the
	// //go:embed above has already proved exists.
	sub, err := fs.Sub(assets, "static")
	if err != nil {
		panic("web: embedded static/ is unreachable: " + err.Error())
	}
	files := http.FileServer(http.FS(sub))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if _, err := fs.Stat(sub, name(r.URL.Path)); err != nil {
			http.NotFound(w, r)
			return
		}
		files.ServeHTTP(w, r)
	})
}

// name turns a URL path into the fs.FS name it addresses: rooted, cleaned,
// and with the leading slash removed, because an fs.FS name is relative and
// "/index.html" is not a valid one. The empty path and "/" both become ".",
// the embedded root, which is how a bare "/" reaches index.html.
//
// path.Clean is what disposes of traversal: "/../assets.go" cleans to
// "/assets.go", which names nothing in the embedded tree.
func name(urlPath string) string {
	if !strings.HasPrefix(urlPath, "/") {
		urlPath = "/" + urlPath
	}
	cleaned := strings.TrimPrefix(path.Clean(urlPath), "/")
	if cleaned == "" {
		return "."
	}
	return cleaned
}
