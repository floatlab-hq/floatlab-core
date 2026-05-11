package hostd

import "os"

func openFile(path string, perm uint32) (*os.File, error) {
	return os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, os.FileMode(perm))
}
