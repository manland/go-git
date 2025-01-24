// Package revlist provides support to access the ancestors of commits, in a
// similar way as the git-rev-list command.
package revlist

import (
	"fmt"
	"io"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/protocol/packp"
	"github.com/go-git/go-git/v5/plumbing/storer"
)

// Objects applies a complementary set. It gets all the hashes from all
// the reachable objects from the given objects. Ignore param are object hashes
// that we want to ignore on the result. All that objects must be accessible
// from the object storer.
func Objects(
	s storer.EncodedObjectStorer,
	objs,
	ignore []plumbing.Hash,
) ([]plumbing.Hash, error) {
	return ObjectsWithStorageForIgnores(s, s, objs, ignore)
}

// ObjectsWithStorageForIgnores is the same as Objects, but a
// secondary storage layer can be provided, to be used to finding the
// full set of objects to be ignored while finding the reachable
// objects.  This is useful when the main `s` storage layer is slow
// and/or remote, while the ignore list is available somewhere local.
func ObjectsWithStorageForIgnores(
	s, ignoreStore storer.EncodedObjectStorer,
	objs,
	ignore []plumbing.Hash,
) ([]plumbing.Hash, error) {
	i, err := objects(ignoreStore, nil, ignore, nil, true, nil, nil)
	if err != nil {
		return nil, err
	}
	o, err := objects(s, nil, objs, fromHashShallow(i), false, nil, nil)
	if err != nil {
		return nil, err
	}
	return fromHashShallow(o), nil
}

type HashShallow struct {
	Hash      plumbing.Hash
	IsShallow bool
}

func fromHashShallow(hashs []HashShallow) []plumbing.Hash {
	res := make([]plumbing.Hash, len(hashs))
	for i, h := range hashs {
		res[i] = h.Hash
	}
	return res
}

// ObjectsWithShallow is the same as Objects but return also shallow
// commits.
func ObjectsWithShallow(
	s storer.Storer,
	objs []plumbing.Hash,
	ignore []plumbing.Hash,
	depth packp.Depth,
	shallows []plumbing.Hash,
) ([]HashShallow, error) {
	return objects(s, s, objs, ignore, false, depth, shallows)
}

func objects(
	s storer.EncodedObjectStorer,
	ss storer.ReferenceStorer,
	objects,
	ignore []plumbing.Hash,
	allowMissingObjects bool,
	depth packp.Depth,
	shallows []plumbing.Hash,
) ([]HashShallow, error) {
	seen := hashListToSet(ignore)
	result := make(map[plumbing.Hash][]plumbing.Hash)
	visited := make(map[plumbing.Hash]bool)

	walkerFunc := func(h plumbing.Hash, parents []plumbing.Hash) {
		if !seen[h] {
			result[h] = parents
			seen[h] = true
		}
	}

	for _, h := range objects {
		if err := processObject(s, ss, h, seen, visited, ignore, walkerFunc, depth, shallows); err != nil {
			if allowMissingObjects && err == plumbing.ErrObjectNotFound {
				continue
			}

			return nil, err
		}
	}

	return hashSetToList(result), nil
}

// processObject obtains the object using the hash an process it depending of its type
func processObject(
	s storer.EncodedObjectStorer,
	ss storer.ReferenceStorer,
	h plumbing.Hash,
	seen map[plumbing.Hash]bool,
	visited map[plumbing.Hash]bool,
	ignore []plumbing.Hash,
	walkerFunc func(h plumbing.Hash, parents []plumbing.Hash),
	depth packp.Depth,
	shallows []plumbing.Hash,
) error {
	o, err := s.EncodedObject(plumbing.AnyObject, h)
	if err != nil {
		return err
	}

	do, err := object.DecodeObject(s, o)
	if err != nil {
		return err
	}

	switch do := do.(type) {
	case *object.Commit:
		return reachableObjects(ss, do, seen, visited, ignore, walkerFunc, depth, shallows)
	case *object.Tree:
		return iterateCommitTrees(seen, do, walkerFunc)
	case *object.Tag:
		walkerFunc(do.Hash, []plumbing.Hash{})
		return processObject(s, ss, do.Target, seen, visited, ignore, walkerFunc, depth, shallows)
	case *object.Blob:
		walkerFunc(do.Hash, []plumbing.Hash{})
	default:
		return fmt.Errorf("object type not valid: %s. "+
			"Object reference: %s", o.Type(), o.Hash())
	}

	return nil
}

// reachableObjects returns, using the callback function, all the reachable
// objects from the specified commit. To avoid to iterate over seen commits,
// if a commit hash is into the 'seen' set, we will not iterate all his trees
// and blobs objects.
func reachableObjects(
	ss storer.ReferenceStorer,
	commit *object.Commit,
	seen map[plumbing.Hash]bool,
	visited map[plumbing.Hash]bool,
	ignore []plumbing.Hash,
	cb func(h plumbing.Hash, parents []plumbing.Hash),
	depth packp.Depth,
	shallows []plumbing.Hash,
) error {
	i := object.NewCommitPreorderIter(commit, seen, ignore)
	if depth != nil && !depth.IsZero() {
		// in depth scenario we need to continue to parents even if child is know
		i = object.NewCommitPreorderIter(commit, nil, nil)
		switch d := depth.(type) {
		case packp.DepthCommits:
			i = object.NewCommitLimitIterFromIter(i, object.LogLimitOptions{Nb: int(d)})
		case packp.DepthSince:
			when := time.Time(d).UTC()
			i = object.NewCommitLimitIterFromIter(i, object.LogLimitOptions{Since: &when})
		case packp.DepthReference:
			ref, err := storer.ResolveReference(ss, plumbing.NewBranchReferenceName(string(d)))
			if err != nil {
				ref, err = storer.ResolveReference(ss, plumbing.NewTagReferenceName(string(d)))
			}
			if err == nil {
				i = object.NewCommitLimitIterFromIter(i, object.LogLimitOptions{StopOn: []plumbing.Hash{ref.Hash()}})
			}
		case packp.DepthRelativeCommits:
			if len(shallows) > 0 {
				i = object.NewCommitLimitIterFromIter(i, object.LogLimitOptions{Nb: int(d), StartCountFrom: shallows})
			}
		default:
			return fmt.Errorf("unsupported depth type")
		}
	}

	pending := make(map[plumbing.Hash]bool)
	addPendingParents(pending, visited, commit)
	for {
		commit, err := i.Next()
		if err == io.EOF {
			break
		}

		if err != nil {
			return err
		}

		if pending[commit.Hash] {
			delete(pending, commit.Hash)
		}

		addPendingParents(pending, visited, commit)

		if visited[commit.Hash] && len(pending) == 0 {
			break
		}

		if seen[commit.Hash] {
			continue
		}

		cb(commit.Hash, commit.ParentHashes)

		tree, err := commit.Tree()
		if err != nil {
			return err
		}

		if err := iterateCommitTrees(seen, tree, cb); err != nil {
			return err
		}
	}

	return nil
}

func addPendingParents(pending, visited map[plumbing.Hash]bool, commit *object.Commit) {
	for _, p := range commit.ParentHashes {
		if !visited[p] {
			pending[p] = true
		}
	}
}

// iterateCommitTrees iterate all reachable trees from the given commit
func iterateCommitTrees(
	seen map[plumbing.Hash]bool,
	tree *object.Tree,
	cb func(h plumbing.Hash, parents []plumbing.Hash),
) error {
	if seen[tree.Hash] {
		return nil
	}

	cb(tree.Hash, []plumbing.Hash{})

	treeWalker := object.NewTreeWalker(tree, true, seen)

	for {
		_, e, err := treeWalker.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}

		if e.Mode == filemode.Submodule {
			continue
		}

		if seen[e.Hash] {
			continue
		}

		cb(e.Hash, []plumbing.Hash{})
	}

	return nil
}

func hashSetToList(hashes map[plumbing.Hash][]plumbing.Hash) []HashShallow {
	var result []HashShallow
	for key, parents := range hashes {
		isShallow := false
		if len(parents) > 0 {
			foundParent := false
			for _, h := range parents {
				if _, ok := hashes[h]; ok {
					foundParent = true
					break
				}
			}
			isShallow = !foundParent
		}
		result = append(result, HashShallow{Hash: key, IsShallow: isShallow})
	}

	return result
}

func hashListToSet(hashes []plumbing.Hash) map[plumbing.Hash]bool {
	result := make(map[plumbing.Hash]bool)
	for _, h := range hashes {
		result[h] = true
	}

	return result
}

// ObjectsWithRef find all hashes linked to objs
// return a map of hashes containing an array of hash objs
func ObjectsWithRef(
	s storer.EncodedObjectStorer,
	objs,
	ignore []plumbing.Hash,
) (map[plumbing.Hash][]plumbing.Hash, error) {
	all := map[plumbing.Hash][]plumbing.Hash{}
	for _, obj := range objs {
		walkerFunc := func(h plumbing.Hash, parents []plumbing.Hash) {
			if hashes, ok := all[h]; ok {
				all[h] = append(hashes, obj)
			} else {
				all[h] = []plumbing.Hash{obj}
			}
		}
		if err := processObject(s, nil, obj, map[plumbing.Hash]bool{}, map[plumbing.Hash]bool{}, ignore, walkerFunc, nil, nil); err != nil {
			return nil, err
		}
	}
	return all, nil
}
