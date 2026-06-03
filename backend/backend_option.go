package backend

import (
	"github.com/go-git/go-git/v6/plumbing/format/packfile"
	"github.com/go-git/go-git/v6/plumbing/transport"
)

// Option configure a Backend
type Option func(*Backend)

func WithHook(hooks transport.Hooks) Option {
	return func(b *Backend) {
		b.Hooks = hooks
	}
}

func WithParserObserver(parserObserver packfile.Observer) Option {
	return func(b *Backend) {
		b.ParserObserver = parserObserver
	}
}
