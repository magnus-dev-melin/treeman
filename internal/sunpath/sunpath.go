package sunpath

import "fmt"

type TooLongError struct {
	Path string
}

func (e *TooLongError) Error() string {
	return fmt.Sprintf("unix socket path %q is %d bytes, over this platform's %d-byte sun_path limit", e.Path, len(e.Path), Max)
}

func Check(path string) error {
	if len(path) > Max {
		return &TooLongError{Path: path}
	}
	return nil
}
