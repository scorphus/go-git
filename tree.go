package git

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"gopkg.in/src-d/go-git.v4/core"
)

const (
	maxTreeDepth = 1024
)

// New errors defined by this package.
var (
	ErrMaxTreeDepth = errors.New("maximum tree depth exceeded")
	ErrFileNotFound = errors.New("file not found")
)

// Tree is basically like a directory - it references a bunch of other trees
// and/or blobs (i.e. files and sub-directories)
type Tree struct {
	Entries []TreeEntry
	Hash    core.Hash

	r *Repository
	m map[string]*TreeEntry
}

// TreeEntry represents a file
type TreeEntry struct {
	Name string
	Mode os.FileMode
	Hash core.Hash
}

// File returns the hash of the file identified by the `path` argument.
// The path is interpreted as relative to the tree receiver.
func (t *Tree) File(path string) (*File, error) {
	e, err := t.findEntry(path)
	if err != nil {
		return nil, ErrFileNotFound
	}

	obj, err := t.r.s.ObjectStorage().Get(e.Hash)
	if err != nil {
		if err == core.ErrObjectNotFound {
			return nil, ErrFileNotFound // a git submodule
		}
		return nil, err
	}

	if obj.Type() != core.BlobObject {
		return nil, ErrFileNotFound // a directory
	}

	blob := &Blob{}
	blob.Decode(obj)

	return newFile(path, e.Mode, blob), nil
}

func (t *Tree) findEntry(path string) (*TreeEntry, error) {
	pathParts := strings.Split(path, "/")

	var tree *Tree
	var err error
	for tree = t; len(pathParts) > 1; pathParts = pathParts[1:] {
		if tree, err = tree.dir(pathParts[0]); err != nil {
			return nil, err
		}
	}

	return tree.entry(pathParts[0])
}

var errDirNotFound = errors.New("directory not found")

func (t *Tree) dir(baseName string) (*Tree, error) {
	entry, err := t.entry(baseName)
	if err != nil {
		return nil, errDirNotFound
	}

	obj, err := t.r.s.ObjectStorage().Get(entry.Hash)
	if err != nil {
		if err == core.ErrObjectNotFound { // git submodule
			return nil, errDirNotFound
		}
		return nil, err
	}

	if obj.Type() != core.TreeObject {
		return nil, errDirNotFound // a file
	}

	tree := &Tree{r: t.r}
	tree.Decode(obj)

	return tree, nil
}

var errEntryNotFound = errors.New("entry not found")

func (t *Tree) entry(baseName string) (*TreeEntry, error) {
	if t.m == nil {
		t.buildMap()
	}
	entry, ok := t.m[baseName]
	if !ok {
		return nil, errEntryNotFound
	}

	return entry, nil
}

// Files returns a FileIter allowing to iterate over the Tree
func (t *Tree) Files() *FileIter {
	return NewFileIter(t.r, t)
}

// ID returns the object ID of the tree. The returned value will always match
// the current value of Tree.Hash.
//
// ID is present to fulfill the Object interface.
func (t *Tree) ID() core.Hash {
	return t.Hash
}

// Type returns the type of object. It always returns core.TreeObject.
func (t *Tree) Type() core.ObjectType {
	return core.TreeObject
}

// Decode transform an core.Object into a Tree struct
func (t *Tree) Decode(o core.Object) (err error) {
	if o.Type() != core.TreeObject {
		return ErrUnsupportedObject
	}

	t.Hash = o.Hash()
	if o.Size() == 0 {
		return nil
	}

	t.Entries = nil
	t.m = nil

	reader, err := o.Reader()
	if err != nil {
		return err
	}
	defer checkClose(reader, &err)

	r := bufio.NewReader(reader)
	for {
		mode, err := r.ReadString(' ')
		if err != nil {
			if err == io.EOF {
				break
			}

			return err
		}

		fm, err := t.decodeFileMode(mode[:len(mode)-1])
		if err != nil && err != io.EOF {
			return err
		}

		name, err := r.ReadString(0)
		if err != nil && err != io.EOF {
			return err
		}

		var hash core.Hash
		if _, err = io.ReadFull(r, hash[:]); err != nil {
			return err
		}

		baseName := name[:len(name)-1]
		t.Entries = append(t.Entries, TreeEntry{
			Hash: hash,
			Mode: fm,
			Name: baseName,
		})
	}

	return nil
}

func (t *Tree) decodeFileMode(mode string) (os.FileMode, error) {
	fm, err := strconv.ParseInt(mode, 8, 32)
	if err != nil && err != io.EOF {
		return 0, err
	}

	m := os.FileMode(fm)
	switch fm {
	case 0040000: //tree
		m = m | os.ModeDir
	case 0120000: //symlink
		m = m | os.ModeSymlink
	}

	return m, nil
}

// Encode transforms a Tree into a core.Object.
func (t *Tree) Encode(o core.Object) error {
	o.SetType(core.TreeObject)
	w, err := o.Writer()
	if err != nil {
		return err
	}

	var size int
	defer checkClose(w, &err)
	for _, entry := range t.Entries {
		n, err := fmt.Fprintf(w, "%o %s", entry.Mode, entry.Name)
		if err != nil {
			return err
		}

		size += n
		n, err = w.Write([]byte{0x00})
		if err != nil {
			return err
		}

		size += n
		n, err = w.Write([]byte(entry.Hash[:]))
		if err != nil {
			return err
		}
		size += n
	}

	o.SetSize(int64(size))
	return err
}

func (t *Tree) buildMap() {
	t.m = make(map[string]*TreeEntry)
	for i := 0; i < len(t.Entries); i++ {
		t.m[t.Entries[i].Name] = &t.Entries[i]
	}
}

// treeEntryIter facilitates iterating through the TreeEntry objects in a Tree.
type treeEntryIter struct {
	t   *Tree
	pos int
}

func (iter *treeEntryIter) Next() (TreeEntry, error) {
	if iter.pos >= len(iter.t.Entries) {
		return TreeEntry{}, io.EOF
	}
	iter.pos++
	return iter.t.Entries[iter.pos-1], nil
}

// TreeEntryIter facilitates iterating through the descendent subtrees of a
// Tree.
type TreeIter struct {
	w TreeWalker
}

// NewTreeIter returns a new TreeIter instance
func NewTreeIter(r *Repository, t *Tree) *TreeIter {
	return &TreeIter{
		w: *NewTreeWalker(r, t),
	}
}

// Next returns the next Tree from the tree.
func (iter *TreeIter) Next() (*Tree, error) {
	for {
		_, _, obj, err := iter.w.Next()
		if err != nil {
			return nil, err
		}

		if tree, ok := obj.(*Tree); ok {
			return tree, nil
		}
	}
}

// ForEach call the cb function for each tree contained on this iter until
// an error happends or the end of the iter is reached. If core.ErrStop is sent
// the iteration is stop but no error is returned. The iterator is closed.
func (iter *TreeIter) ForEach(cb func(*Tree) error) error {
	defer iter.Close()

	for {
		t, err := iter.Next()
		if err != nil {
			if err == io.EOF {
				return nil
			}

			return err
		}

		if err := cb(t); err != nil {
			if err == core.ErrStop {
				return nil
			}

			return err
		}
	}
}

// Close closes the TreeIter
func (iter *TreeIter) Close() {
	iter.w.Close()
}
