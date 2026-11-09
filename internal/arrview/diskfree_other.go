//go:build !unix

package arrview

func diskFree(string) (free, total int64, ok bool) { return 0, 0, false }
