//go:build !unix

package icons

import "os"

// openIcon has no O_NOFOLLOW to lean on here; Open's Lstat and SameFile
// checks are what keep a link out.
func openIcon(p string) (*os.File, error) { return os.Open(p) }
