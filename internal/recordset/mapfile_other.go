//go:build !unix

package recordset

// mapFile loads path into memory on platforms without a tested mapping.
func mapFile(path string) (data []byte, release func() error, err error) {
	return readFile(path)
}
