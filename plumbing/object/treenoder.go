package object

import (
	"encoding/binary"

	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/filemode"
	"github.com/go-git/go-git/v6/utils/merkletrie/noder"
)

// A treenoder is a helper type that wraps git trees into merkletrie
// noders.
//
// As a merkletrie noder doesn't understand the concept of modes (e.g.
// file permissions), the treenoder includes the mode of the git tree in
// the hash, so changes in the modes will be detected as modifications
// to the file contents by the merkletrie difftree algorithm.  This is
// consistent with how the "git diff-tree" command works.
type treeNoder struct {
	parent   *Tree  // the root node is its own parent
	name     string // empty string for the root node
	mode     filemode.FileMode
	hash     plumbing.Hash
	children []noder.Noder // memoized
}

const encodedFileModeSize = 4

// NewTreeRootNode returns the root node of a Tree
func NewTreeRootNode(t *Tree) noder.Noder {
	if t == nil {
		return &treeNoder{}
	}

	return &treeNoder{
		parent: t,
		name:   "",
		mode:   filemode.Dir,
		hash:   t.Hash,
	}
}

func (t *treeNoder) Skip() bool {
	return false
}

func (t *treeNoder) isRoot() bool {
	return t.name == ""
}

func (t *treeNoder) String() string {
	return "treeNoder <" + t.name + ">"
}

func (t *treeNoder) Hash() []byte {
	mode := t.mode
	if mode == filemode.Deprecated {
		mode = filemode.Regular
	}
	size := t.hash.Size()
	combined := make([]byte, size+encodedFileModeSize)
	copy(combined, t.hash.Bytes())
	binary.LittleEndian.PutUint32(combined[size:], uint32(mode))
	return combined
}

func (t *treeNoder) Name() string {
	return t.name
}

func (t *treeNoder) IsDir() bool {
	return t.mode == filemode.Dir
}

// Children will return the children of a treenoder as treenoders,
// building them from the children of the wrapped git tree.
func (t *treeNoder) Children() ([]noder.Noder, error) {
	if t.mode != filemode.Dir {
		return noder.NoChildren, nil
	}

	// children are memoized for efficiency
	if t.children != nil {
		return t.children, nil
	}

	// the parent of the returned children will be ourself as a tree if
	// we are a not the root treenoder.  The root is special as it
	// is is own parent.
	parent := t.parent
	if !t.isRoot() {
		var err error
		if parent, err = GetTree(t.parent.s, t.hash); err != nil {
			return nil, err
		}
	}

	t.children = transformChildren(parent)
	return t.children, nil
}

// Returns the children of a tree as treenoders.
// Efficiency is key here.
func transformChildren(t *Tree) []noder.Noder {
	ret := make([]noder.Noder, len(t.Entries))
	for i, e := range t.Entries {
		ret[i] = &treeNoder{
			parent: t,
			name:   e.Name,
			mode:   e.Mode,
			hash:   e.Hash,
		}
	}
	return ret
}

func (t *treeNoder) NumChildren() (int, error) {
	children, err := t.Children()
	if err != nil {
		return 0, err
	}

	return len(children), nil
}
