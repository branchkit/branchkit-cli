package main

// A decoder for Snappy's block format, the compression GitHub stores
// attestation bundles in: the attestations API answers with a `bundle_url`
// to a `.json.sn` blob instead of the bundle itself. The format is a varint
// of the decoded length, then elements, each a literal run or a copy of
// earlier output (https://github.com/google/snappy/blob/main/format_description.txt).
// Decoding is a few dozen lines, so it is written here rather than taken as
// a dependency of the install path.

import (
	"encoding/binary"
	"errors"
	"fmt"
)

var errSnappyCorrupt = errors.New("snappy: corrupt input")

// snappyDecode decodes one Snappy block, refusing any whose decoded length
// is over limit before allocating it.
func snappyDecode(src []byte, limit int) ([]byte, error) {
	n, k := binary.Uvarint(src)
	if k <= 0 {
		return nil, errSnappyCorrupt
	}
	if n > uint64(limit) {
		return nil, fmt.Errorf("snappy: decoded length %d is over %d", n, limit)
	}
	src = src[k:]
	dst := make([]byte, 0, int(n))
	for len(src) > 0 {
		tag := src[0]
		var length, offset int
		switch tag & 3 {
		case 0: // literal: length-1 in the tag, or in 1-4 bytes after it
			length = int(tag >> 2)
			extra := 0
			if length >= 60 {
				extra = length - 59
				if len(src) < 1+extra {
					return nil, errSnappyCorrupt
				}
				length = 0
				for i := 0; i < extra; i++ {
					length |= int(src[1+i]) << (8 * i)
				}
			}
			length++
			src = src[1+extra:]
			if length <= 0 || length > len(src) || len(dst)+length > int(n) {
				return nil, errSnappyCorrupt
			}
			dst = append(dst, src[:length]...)
			src = src[length:]
			continue
		case 1: // copy, 11-bit offset
			if len(src) < 2 {
				return nil, errSnappyCorrupt
			}
			length = 4 + int(tag>>2)&7
			offset = int(tag&0xe0)<<3 | int(src[1])
			src = src[2:]
		case 2: // copy, 16-bit offset
			if len(src) < 3 {
				return nil, errSnappyCorrupt
			}
			length = 1 + int(tag>>2)
			offset = int(binary.LittleEndian.Uint16(src[1:3]))
			src = src[3:]
		case 3: // copy, 32-bit offset
			if len(src) < 5 {
				return nil, errSnappyCorrupt
			}
			length = 1 + int(tag>>2)
			offset = int(binary.LittleEndian.Uint32(src[1:5]))
			src = src[5:]
		}
		if offset <= 0 || offset > len(dst) || len(dst)+length > int(n) {
			return nil, errSnappyCorrupt
		}
		// A copy may overlap what it writes (offset < length): byte by byte.
		start := len(dst) - offset
		for i := 0; i < length; i++ {
			dst = append(dst, dst[start+i])
		}
	}
	if len(dst) != int(n) {
		return nil, errSnappyCorrupt
	}
	return dst, nil
}
