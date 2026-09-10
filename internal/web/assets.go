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
)

//go:embed static
var assets embed.FS

// Assets is the front end, ready to mount: the server skeleton gives it "/",
// and a browser asking for the root gets static/index.html.
func Assets() http.Handler {
	// Strip the static/ prefix, so a request for "/" resolves to index.html
	// rather than needing "/static/index.html". fs.Sub fails only on a
	// malformed path, and this one is a compile-time constant that the
	// //go:embed above has already proved exists.
	sub, err := fs.Sub(assets, "static")
	if err != nil {
		panic("web: embedded static/ is unreachable: " + err.Error())
	}
	return http.FileServer(http.FS(sub))
}
