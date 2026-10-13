package problem

// Error is the shared service error: services return it and the API layer maps it onto a problem+json
// body. It is a plain value type; the leaf package imports nothing from this module.
type Error struct {
	Status  int
	Code    string
	Message string
	Details map[string]any
}

// Error implements error as "CODE: message".
func (e *Error) Error() string { return e.Code + ": " + e.Message }

// E builds an *Error.
func E(status int, code, msg string, details map[string]any) *Error {
	return &Error{Status: status, Code: code, Message: msg, Details: details}
}
