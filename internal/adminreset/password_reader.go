package adminreset

import (
	"bytes"
	"io"
	"os"
)

func readPasswordLine(file *os.File) ([]byte, error) {
	var line []byte
	var one [1]byte
	for {
		n, err := file.Read(one[:])
		if n > 0 {
			line = append(line, one[0])
			if one[0] == '\n' {
				return bytes.TrimRight(line, "\r\n"), nil
			}
		}
		if err != nil {
			if err == io.EOF && len(line) > 0 {
				return bytes.TrimRight(line, "\r\n"), nil
			}
			return nil, err
		}
	}
}
