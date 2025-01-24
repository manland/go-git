package object

import (
	"io"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/storer"
)

type commitLimitIter struct {
	sourceIter            CommitIter
	limitOptions          LogLimitOptions
	nb                    int
	startCountFromReached bool
	stopOnReached         bool
}

type LogLimitOptions struct {
	Since          *time.Time
	Until          *time.Time
	TailHash       plumbing.Hash
	Nb             int // 0 means no limit
	StartCountFrom []plumbing.Hash
	StopOn         []plumbing.Hash
}

func NewCommitLimitIterFromIter(commitIter CommitIter, limitOptions LogLimitOptions) CommitIter {
	iterator := new(commitLimitIter)
	iterator.sourceIter = commitIter
	iterator.limitOptions = limitOptions
	return iterator
}

func (c *commitLimitIter) Next() (*Commit, error) {
	for {
		commit, err := c.sourceIter.Next()
		if err != nil {
			return nil, err
		}

		if c.limitOptions.StartCountFrom != nil || c.startCountFromReached {
			if c.startCountFromReached {
				c.nb++
			} else {
				for _, h := range c.limitOptions.StartCountFrom {
					if commit.Hash.String() == h.String() {
						c.startCountFromReached = true
						break
					}
				}
			}
		} else {
			c.nb++
		}
		if c.limitOptions.Nb > 0 && c.nb > c.limitOptions.Nb {
			c.sourceIter.Close()
			return nil, io.EOF
		}

		if c.limitOptions.StopOn != nil {
			if c.stopOnReached {
				c.sourceIter.Close()
				return nil, io.EOF
			}
			for _, h := range c.limitOptions.StopOn {
				if commit.Hash.String() == h.String() {
					c.stopOnReached = true
				}
			}
		}

		if c.limitOptions.Since != nil && commit.Committer.When.Before(*c.limitOptions.Since) {
			continue
		}
		if c.limitOptions.Until != nil && commit.Committer.When.After(*c.limitOptions.Until) {
			continue
		}
		if c.limitOptions.TailHash == commit.Hash {
			return commit, storer.ErrStop
		}
		return commit, nil
	}
}

func (c *commitLimitIter) ForEach(cb func(*Commit) error) error {
	for {
		commit, nextErr := c.Next()
		if nextErr == io.EOF {
			break
		}
		if nextErr != nil && nextErr != storer.ErrStop {
			return nextErr
		}
		err := cb(commit)
		if err == storer.ErrStop || nextErr == storer.ErrStop {
			return nil
		} else if err != nil {
			return err
		}
	}
	return nil
}

func (c *commitLimitIter) Close() {
	c.sourceIter.Close()
}
