package i18n

// HasZh reports whether a Chinese translation is registered for the English
// source string msgID. The translation tables are keyed by the English text,
// so a source string that is edited without updating zh_*.go silently falls
// back to English; callers that can enumerate their own strings (see cmd's
// i18n coverage test) use this to turn that silent miss into a failure.
func HasZh(msgID string) bool {
	_, ok := zhTable[msgID]
	return ok
}

// CoveredByZh reports whether s takes part in translation under either
// direction: as an English source string (key) or as rendered Chinese (value).
// Text translated in place — command group titles, for instance — has already
// lost its English original by the time a caller can see it, and the language
// it was rendered in depends on how the process was started, so checking both
// directions is what makes such a string assertable.
func CoveredByZh(s string) bool {
	if _, ok := zhTable[s]; ok {
		return true
	}
	for _, v := range zhTable {
		if v == s {
			return true
		}
	}
	return false
}
