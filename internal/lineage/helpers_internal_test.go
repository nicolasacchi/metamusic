package lineage

import "errors"

func asParseError(err error, target **ParseError) bool { return errors.As(err, target) }
