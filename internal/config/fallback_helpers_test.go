package config

import "fmt"

func fmtAll(v any) string { return fmt.Sprintf("%v|%+v|%#v|%s", v, v, v, v) }
