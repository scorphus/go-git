package git

import (
	"bytes"
	"io"
	"os"
	"strings"

	"gopkg.in/src-d/go-git.v4/core"
)

// File represents git file objects.
type File struct {
	Name string
	Mode os.FileMode
	Blob
}

func newFile(name string, m os.FileMode, b *Blob) *File {
	return &File{Name: name, Mode: m, Blob: *b}
}

// Contents returns the contents of a file as a string.
func (f *File) Contents() (content string, err error) {
	reader, err := f.Reader()
	if err != nil {
		return "", err
	}
	defer checkClose(reader, &err)

	buf := new(bytes.Buffer)
	buf.ReadFrom(reader)

	return buf.String(), nil
}

// Lines returns a slice of lines from the contents of a file, stripping
// all end of line characters. If the last line is empty (does not end
// in an end of line), it is also stripped.
func (f *File) Lines() ([]string, error) {
	content, err := f.Contents()
	if err != nil {
		return nil, err
	}

	splits := strings.Split(content, "\n")
	// remove the last line if it is empty
	if splits[len(splits)-1] == "" {
		return splits[:len(splits)-1], nil
	}

	return splits, nil
}

type FileIter struct {
	w TreeWalker
}

func NewFileIter(r *Repository, t *Tree) *FileIter {
	return &FileIter{w: *NewTreeWalker(r, t)}
}

func (iter *FileIter) Next() (*File, error) {
	for {
		name, entry, obj, err := iter.w.Next()
		if err != nil {
			return nil, err
		}

		if blob, ok := obj.(*Blob); ok {
			return newFile(name, entry.Mode, blob), nil
		}
	}
}

// ForEach call the cb function for each file contained on this iter until
// an error happends or the end of the iter is reached. If core.ErrStop is sent
// the iteration is stop but no error is returned
func (iter *FileIter) ForEach(cb func(*File) error) error {
	i := &FileIter{w: *NewTreeWalker(iter.w.r, iter.w.t)}
	defer i.Close()

	for {
		f, err := i.Next()
		if err != nil {
			if err == io.EOF {
				return nil
			}

			return err
		}

		if err := cb(f); err != nil {
			if err == core.ErrStop {
				return nil
			}

			return err
		}
	}
}

func (iter *FileIter) Close() {
	iter.w.Close()
}
