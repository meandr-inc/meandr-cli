package pty

import (
	"errors"
	"os"
	"path/filepath"
)

// maxReason bounds the reason a failed answer carries.
const maxReason = 512

var errName = errors.New("not a plain file name")

// upload is a pasted file on its way to disk: a hidden partial file, renamed
// into place once its last byte is in.
type upload struct {
	path string
	f    *os.File // nil once failed: the rest of its bytes are dropped
	left int64
	err  error
}

// startUpload opens the partial file for name in dir. A failure is kept for
// the answer, and the file's bytes are still read past.
func startUpload(dir, name string, size int64) *upload {
	u := &upload{path: filepath.Join(dir, name), left: size}
	if name == "" || name != filepath.Base(name) || name[0] == '.' {
		u.err = errName
		return u
	}
	if u.err = os.MkdirAll(dir, 0o700); u.err != nil {
		return u
	}
	u.f, u.err = os.OpenFile(filepath.Join(dir, "."+name+".part"), os.O_WRONLY|os.O_CREATE|os.O_EXCL|noFollow, 0o600)
	return u
}

func (u *upload) write(b []byte) {
	u.left -= int64(len(b))
	if u.f == nil {
		return
	}
	if _, err := u.f.Write(b); err != nil {
		u.err = err
		u.abort()
	}
}

// abort removes the partial file, so nothing of the upload stays.
func (u *upload) abort() {
	if u.f != nil {
		_ = u.f.Close()
		_ = os.Remove(u.f.Name())
		u.f = nil
	}
}

// finish renames the whole file into place and answers with its path, or
// with why it is not there.
func (u *upload) finish() []byte {
	if u.f != nil {
		part := u.f.Name()
		err := u.f.Close()
		u.f = nil
		if err == nil {
			err = os.Rename(part, u.path)
		}
		if err == nil {
			return newFrame(frameFileSaved, []byte(u.path))
		}
		_ = os.Remove(part)
		u.err = err
	}
	reason := u.err.Error()
	if len(reason) > maxReason {
		reason = reason[:maxReason]
	}
	return newFrame(frameFileFailed, []byte(reason))
}
