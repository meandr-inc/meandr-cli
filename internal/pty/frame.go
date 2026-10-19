package pty

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// Frames, each behind a type and a 4-byte big-endian length: data (1), resize
// (2, columns and rows in the length's place); with --storage, a pasted file
// (4: its size, then its name), its data (5), and the answer, saved (6) or failed (7).
const (
	frameData       = 1
	frameResize     = 2
	frameFile       = 4
	frameFileData   = 5
	frameFileSaved  = 6
	frameFileFailed = 7
	headerLen       = 5

	// maxData bounds a data frame's bytes, and maxName a pasted file's name.
	maxData = 64 << 10
	maxName = 255
)

// errFrame is a frame this side does not accept: the stream ends on it.
var errFrame = errors.New("pty: bad frame")

type frame struct {
	kind       byte
	data       []byte // a data frame's bytes, valid until the next frame is read
	cols, rows uint16 // a resize frame's size
	size       uint32 // a file frame's size and name
	name       string
}

// frameReader decodes a stream's frames into one buffer it reuses.
type frameReader struct {
	r   io.Reader
	hdr [headerLen]byte
	buf []byte
}

func (fr *frameReader) next() (frame, error) {
	if _, err := io.ReadFull(fr.r, fr.hdr[:]); err != nil {
		return frame{}, err
	}

	switch fr.hdr[0] {
	case frameData, frameFileData:
		n := binary.BigEndian.Uint32(fr.hdr[1:])
		if n > maxData {
			return frame{}, fmt.Errorf("%w: %d bytes of data, over %d", errFrame, n, maxData)
		}
		data, err := fr.payload(n)
		if err != nil {
			return frame{}, err
		}
		return frame{kind: fr.hdr[0], data: data}, nil

	case frameFile:
		n := binary.BigEndian.Uint32(fr.hdr[1:])
		if n < 4 || n > 4+maxName {
			return frame{}, fmt.Errorf("%w: a file frame of %d bytes", errFrame, n)
		}
		b, err := fr.payload(n)
		if err != nil {
			return frame{}, err
		}
		return frame{kind: frameFile, size: binary.BigEndian.Uint32(b[:4]), name: string(b[4:])}, nil

	case frameResize:
		return frame{
			kind: frameResize,
			cols: binary.BigEndian.Uint16(fr.hdr[1:3]),
			rows: binary.BigEndian.Uint16(fr.hdr[3:5]),
		}, nil

	default:
		return frame{}, fmt.Errorf("%w: type %d", errFrame, fr.hdr[0])
	}
}

// payload reads a frame's n bytes into the reader's buffer.
func (fr *frameReader) payload(n uint32) ([]byte, error) {
	if uint32(cap(fr.buf)) < n {
		fr.buf = make([]byte, n)
	}
	b := fr.buf[:n]
	if _, err := io.ReadFull(fr.r, b); err != nil {
		return nil, err
	}
	return b, nil
}

// dataFrame copies p into a new data frame.
func dataFrame(p []byte) []byte { return newFrame(frameData, p) }

func newFrame(kind byte, p []byte) []byte {
	f := make([]byte, headerLen+len(p))
	f[0] = kind
	binary.BigEndian.PutUint32(f[1:headerLen], uint32(len(p)))
	copy(f[headerLen:], p)
	return f
}
