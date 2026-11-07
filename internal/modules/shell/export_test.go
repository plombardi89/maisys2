package shell

import "github.com/plombardi89/maisys2/internal/resource"

// Unwrap returns what r adds checks to, if the shell module wraps it: a
// git checkout it verifies, or a file it adds notes to. Otherwise it
// returns r.
func Unwrap(r resource.Resource) resource.Resource {
	switch r := r.(type) {
	case *verified:
		return r.Checkout
	case *noted:
		return r.File
	default:
		return r
	}
}
