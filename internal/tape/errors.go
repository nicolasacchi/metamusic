package tape

import "fmt"

// RenderError is a failed render: Code is one of the Err* codes of types.go (what lands in tapes.error_code), Item the
// 1-based position of the item being decoded when it applies (0 otherwise). The message holds codes, positions and the
// tool's own error text, never a title or a path.
type RenderError struct {
	Code string
	Item int
	Err  error
}

func (e *RenderError) Error() string {
	switch {
	case e.Item > 0 && e.Err != nil:
		return fmt.Sprintf("%s: item %d: %v", e.Code, e.Item, e.Err)
	case e.Item > 0:
		return fmt.Sprintf("%s: item %d", e.Code, e.Item)
	case e.Err != nil:
		return fmt.Sprintf("%s: %v", e.Code, e.Err)
	}
	return e.Code
}

func (e *RenderError) Unwrap() error { return e.Err }

func renderErr(code string, item int, err error) *RenderError {
	return &RenderError{Code: code, Item: item, Err: err}
}
