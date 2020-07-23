// Copyright 2015 Google Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package gcsx

import (
	"fmt"
	"io"
	"os"
	"time"

	"github.com/jacobsa/fuse/fsutil"
	"github.com/jacobsa/timeutil"
)

// TempFile is a temporary file that keeps track of the lowest offset at which
// it has been modified.
//
// Not safe for concurrent access.
type TempFile interface {
	// Panic if any internal invariants are violated.
	CheckInvariants()

	// Semantics matching os.File.
	io.ReadSeeker
	io.ReaderAt
	io.WriterAt
	Truncate(n int64) (err error)

	// Return information about the current state of the content. May invalidate
	// the seek position.
	Stat() (sr StatResult, err error)

	// Explicitly set the mtime that will return in stat results. This will stick
	// until another method that modifies the file is called.
	SetMtime(mtime time.Time)

	// Throw away the resources used by the temporary file. The object must not
	// be used again.
	Destroy()
}

// StatResult stores the result of a stat operation.
type StatResult struct {
	// The current size in bytes of the content.
	Size int64

	// The largest value T such that we are sure that the range of bytes [0, T)
	// is unmodified from the original content with which the temp file was
	// created.
	DirtyThreshold int64

	// The mtime of the temp file is updated according to the temp file's clock
	// with each call to a method that modified its content, and is also updated
	// when the user explicitly calls SetMtime.
	//
	// If neither of those things has ever happened, it is nil. This implies that
	// DirtyThreshold == Size.
	Mtime *time.Time
}

// NewTempFile creates a temp file whose initial contents are given by the
// supplied reader. dir is a directory on whose file system the inode will live,
// or the system default temporary location if empty.
func NewTempFile(
	content io.Reader,
	dir string,
	clock timeutil.Clock) (tf TempFile, err error) {
	// Create an anonymous file to wrap. When we close it, its resources will be
	// magically cleaned up.
	f, err := fsutil.AnonymousFile(dir)
	if err != nil {
		err = fmt.Errorf("AnonymousFile: %v", err)
		return
	}

	// Copy into the file.
	size, err := io.Copy(f, content)
	if err != nil {
		err = fmt.Errorf("copy: %v", err)
		return
	}

	tf = &tempFile{
		clock:          clock,
		f:              f,
		dirtyThreshold: size,
	}

	return
}

type tempFile struct {
	/////////////////////////
	// Dependencies
	/////////////////////////

	clock timeutil.Clock

	/////////////////////////
	// Mutable state
	/////////////////////////

	destroyed bool

	// A file containing our current contents.
	f *os.File

	// The lowest byte index that has been modified from the initial contents.
	//
	// INVARIANT: Stat().DirtyThreshold <= Stat().Size
	dirtyThreshold int64

	// The time at which a method that modifies our contents was last called, or
	// nil if never.
	//
	// INVARIANT: mtime == nil => Stat().DirtyThreshold == Stat().Size
	mtime *time.Time
}

////////////////////////////////////////////////////////////////////////
// Public interface
////////////////////////////////////////////////////////////////////////

func (tf *tempFile) CheckInvariants() {
	if tf.destroyed {
		panic("Use of destroyed tempFile object.")
	}

	// Restore the seek position after using Stat below.
	pos, err := tf.Seek(0, 1)
	if err != nil {
		panic(fmt.Sprintf("Seek: %v", err))
	}

	defer func() {
		_, err := tf.Seek(pos, 0)
		if err != nil {
			panic(fmt.Sprintf("Seek: %v", err))
		}
	}()

	// INVARIANT: Stat().DirtyThreshold <= Stat().Size
	sr, err := tf.Stat()
	if err != nil {
		panic(fmt.Sprintf("Stat: %v", err))
	}

	if !(sr.DirtyThreshold <= sr.Size) {
		panic(fmt.Sprintf("Mismatch: %d vs. %d", sr.DirtyThreshold, sr.Size))
	}

	// INVARIANT: mtime == nil => Stat().DirtyThreshold == Stat().Size
	if tf.mtime == nil && sr.DirtyThreshold != sr.Size {
		panic(fmt.Sprintf("Mismatch: %d vs. %d", sr.DirtyThreshold, sr.Size))
	}
}

func (tf *tempFile) Destroy() {
	tf.destroyed = true

	// Throw away the file.
	tf.f.Close()
	tf.f = nil
}

func (tf *tempFile) Read(p []byte) (int, error) {
	return tf.f.Read(p)
}

func (tf *tempFile) Seek(offset int64, whence int) (int64, error) {
	return tf.f.Seek(offset, whence)
}

func (tf *tempFile) ReadAt(p []byte, offset int64) (int, error) {
	return tf.f.ReadAt(p, offset)
}

func (tf *tempFile) Stat() (sr StatResult, err error) {
	sr.DirtyThreshold = tf.dirtyThreshold
	sr.Mtime = tf.mtime

	// Get the size from the file.
	sr.Size, err = tf.f.Seek(0, 2)
	if err != nil {
		err = fmt.Errorf("Seek: %v", err)
		return
	}

	return
}

func (tf *tempFile) WriteAt(p []byte, offset int64) (int, error) {
	// Update our state regarding being dirty.
	tf.dirtyThreshold = minInt64(tf.dirtyThreshold, offset)

	newMtime := tf.clock.Now()
	tf.mtime = &newMtime

	// Call through.
	return tf.f.WriteAt(p, offset)
}

func (tf *tempFile) Truncate(n int64) error {
	// Update our state regarding being dirty.
	tf.dirtyThreshold = minInt64(tf.dirtyThreshold, n)

	newMtime := tf.clock.Now()
	tf.mtime = &newMtime

	// Call through.
	return tf.f.Truncate(n)
}

func (tf *tempFile) SetMtime(mtime time.Time) {
	tf.mtime = &mtime
}

////////////////////////////////////////////////////////////////////////
// Helpers
////////////////////////////////////////////////////////////////////////

func minInt64(a int64, b int64) int64 {
	if a < b {
		return a
	}

	return b
}
