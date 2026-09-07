// Package i18n provides minimal zero-dependency bilingual (English/Chinese)
// support. English source strings are the message IDs; Chinese translations
// live in Go map files (zh_*.go). English is the identity/fallback.
package i18n

import (
	"fmt"
	"os"
	"strings"
	"sync"
)

// Lang is a display language.
type Lang int

const (
	En Lang = iota
	Zh
)

var (
	mu      sync.RWMutex
	current = En
)

// SetLang sets the current display language.
func SetLang(l Lang) {
	mu.Lock()
	current = l
	mu.Unlock()
}

// Current returns the current display language.
func Current() Lang {
	mu.RLock()
	defer mu.RUnlock()
	return current
}

// Normalize maps a language tag to a Lang. Accepts zh, zh-CN, zh_CN, zh-Hans,
// cn (case-insensitive). Anything else maps to En, so an invalid --lang or
// config value degrades gracefully instead of erroring.
func Normalize(s string) Lang {
	switch strings.ToLower(strings.ReplaceAll(strings.TrimSpace(s), "_", "-")) {
	case "zh", "zh-cn", "zh-hans", "cn":
		return Zh
	default:
		return En
	}
}

// SystemLang detects the language from the LC_ALL / LANG environment
// variables. Returns En when unset or not Chinese.
func SystemLang() Lang {
	for _, env := range []string{"LC_ALL", "LANG"} {
		if v := os.Getenv(env); v != "" && strings.HasPrefix(strings.ToLower(v), "zh") {
			return Zh
		}
	}
	return En
}

// zhTable is the merged English->Chinese translation table populated by the
// init() functions in zh_*.go files. Register helps detect duplicate keys.
var zhTable = map[string]string{}

// register folds a translation batch into zhTable. Identical duplicates are
// allowed (same English key mapping to the same Chinese text in two files);
// conflicting duplicates panic so they surface at build/test time.
func register(batch map[string]string) {
	for k, v := range batch {
		if prev, ok := zhTable[k]; ok {
			if prev != v {
				panic("i18n: conflicting translation for " + fmt.Sprintf("%q", k))
			}
			continue
		}
		zhTable[k] = v
	}
}

// T translates msgID to the current language. msgID is the English source
// string and also the fallback when no translation exists. It takes no
// arguments and performs no formatting — use Tf when the message has
// placeholders. Intended for translating whole help blocks verbatim.
func T(msgID string) string {
	if Current() == Zh {
		if t, ok := zhTable[msgID]; ok {
			return t
		}
	}
	return msgID
}

// Tf translates a format string to the current language and formats it with
// args. format is the English source and the fallback. The first argument
// must be a constant literal (all call sites pass literals); go vet relies
// on that when it treats Tf as a printf wrapper.
func Tf(format string, args ...any) string {
	f := format
	if Current() == Zh {
		if t, ok := zhTable[format]; ok {
			f = t
		}
	}
	if len(args) == 0 {
		return f
	}
	return fmt.Sprintf(f, args...)
}
