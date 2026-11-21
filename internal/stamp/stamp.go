// Package stamp reads and writes the set code stamped at the end of the installer file: the last 32 bytes.
//
//	0-15   the set code, padded with 0x00
//	16     code length (8..12)
//	17     format version (1)
//	18-21  CRC-32 (IEEE, big endian) of bytes 0-17
//	22-31  magic "FILEIONSET"
//
// The CRC only catches a damaged copy. The code is public and the list it names is checked as always.
package stamp

import (
	"encoding/binary"
	"errors"
	"hash/crc32"
	"io"
	"os"
	"regexp"
)

// Size is the length of the trailer in bytes.
const Size = 32

const (
	magic   = "FILEIONSET"
	version = 1
)

var codeRe = regexp.MustCompile(`^[a-f0-9]{8,12}$`)

// Encode builds the trailer for a set code.
func Encode(code string) ([]byte, error) {
	if !codeRe.MatchString(code) {
		return nil, errors.New("the set code must be 8 to 12 lower-case hex characters")
	}
	b := make([]byte, Size)
	copy(b, code)
	b[16], b[17] = byte(len(code)), version
	binary.BigEndian.PutUint32(b[18:22], crc32.ChecksumIEEE(b[:18]))
	copy(b[22:], magic)
	return b, nil
}

// Decode returns the code in a trailer, or false when it is not a whole, valid one.
func Decode(b []byte) (string, bool) {
	if len(b) != Size || string(b[22:]) != magic || b[17] != version {
		return "", false
	}
	if binary.BigEndian.Uint32(b[18:22]) != crc32.ChecksumIEEE(b[:18]) {
		return "", false
	}
	n := int(b[16])
	if n < 8 || n > 12 {
		return "", false
	}
	code := string(b[:n])
	if !codeRe.MatchString(code) {
		return "", false
	}
	for _, c := range b[n:16] { // padding must be empty
		if c != 0 {
			return "", false
		}
	}
	return code, true
}

// Read returns the code stamped at the end of the file at path, or "" when there is none (or it is damaged).
func Read(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	end, err := f.Seek(-Size, io.SeekEnd)
	if err != nil || end < 0 {
		return ""
	}
	b := make([]byte, Size)
	if _, err := io.ReadFull(f, b); err != nil {
		return ""
	}
	code, _ := Decode(b)
	return code
}
