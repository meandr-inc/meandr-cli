package pty

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// A stream carries two frames, each behind a 5-byte header. Data is type 1,
// a 4-byte big-endian length, then the bytes; resize is type 2, columns and
// rows as 2-byte big-endian numbers.
const (
	frameData   = 1
	frameResize = 2
	headerLen   = 5

	// maxData bounds a data frame's bytes.
	maxData = 64 << 10
)

// errFrame is a frame this side does not accept: the stream ends on it.
var errFrame = errors.New("pty: bad frame")

type frame struct {
	kind       byte
	data       []byte // a data frame's bytes, valid until the next frame is read
	cols, rows uint16 // a resize frame's size
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
	case frameData:
		n := binary.BigEndian.Uint32(fr.hdr[1:])
		if n > maxData {
			return frame{}, fmt.Errorf("%w: %d bytes of data, over %d", errFrame, n, maxData)
		}
		if uint32(cap(fr.buf)) < n {
			fr.buf = make([]byte, n)
		}
		data := fr.buf[:n]
		if _, err := io.ReadFull(fr.r, data); err != nil {
			return frame{}, err
		}
		return frame{kind: frameData, data: data}, nil

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

// dataFrame copies p into a new data frame.
func dataFrame(p []byte) []byte {
	f := make([]byte, headerLen+len(p))
	f[0] = frameData
	binary.BigEndian.PutUint32(f[1:headerLen], uint32(len(p)))
	copy(f[headerLen:], p)
	return f
}
