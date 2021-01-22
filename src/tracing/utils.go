package tracing

import (
	"os"
	"strconv"
	"strings"
	"time"
)

func GetenvFallback(key, fallbackKey string) string {
	if v, ok := os.LookupEnv(key); !ok {
		return os.Getenv(fallbackKey)
	} else {
		return v
	}
}

func StrOrDef(s, def string) string { return IfElseStr(s != "", s, def) }
func IfElseStr(pred bool, a, b string) string {
	if pred {
		return a
	}
	return b
}

func IntOrDef(v, def int) int { return IfElseInt(v != 0, v, def) }
func IfElseInt(pred bool, a, b int) int {
	if pred {
		return a
	}
	return b
}

func DurOrDef(v, def time.Duration) time.Duration { return IfElseDur(v != 0, v, def) }
func IfElseDur(pred bool, a, b time.Duration) time.Duration {
	if pred {
		return a
	}
	return b
}

func AtoiDefaultOrPanic(s string, def int) int {
	if s == "" {
		return def
	}
	return AtoiOrPanic(s)
}

func AtoiOrPanic(s string) int {
	i, err := strconv.Atoi(s)
	if err != nil {
		panic(err)
	}
	return i
}

func ParseDurationDefaultOrPanic(s string, def time.Duration) time.Duration {
	d, err := ParseDurationDefault(s, def)
	if err != nil {
		panic(err)
	}
	return d
}

func ParseBoolDefaultOrPanic(s string, def bool) bool {
	d, err := ParseBoolDefault(s, def)
	if err != nil {
		panic(err)
	}
	return d
}

func ParseDurationDefault(s string, def time.Duration) (time.Duration, error) {
	if s == "" {
		return def, nil
	}
	return time.ParseDuration(s)
}

func ParseBoolDefault(s string, def bool) (bool, error) {
	if s == "" {
		return def, nil
	}
	return strconv.ParseBool(s)
}

func EscapeColonInString(input string) string {
	return strings.Replace(input, ":", "_", -1)
}


