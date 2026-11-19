package recordset

import "os"

// readFile is the portable stand-in for a mapping: the whole file in memory.
func readFile(path string) (data []byte, release func() error, err error) {
	data, err = os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	return data, func() error { return nil }, nil
}
