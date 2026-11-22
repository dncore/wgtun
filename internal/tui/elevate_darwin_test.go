//go:build darwin

package tui

import "testing"

func TestAppleScriptQuote(t *testing.T) {
	if got := shellQuote("/opt/my wgtun/wgtun"); got != `'/opt/my wgtun/wgtun'` {
		t.Errorf("shellQuote = %q", got)
	}
	if got := shellQuote("/tmp/it's"); got != `'/tmp/it'\''s'` {
		t.Errorf("shellQuote apostrophe = %q", got)
	}
	if got := appleScriptString(`a"b\c`); got != `"a\"b\\c"` {
		t.Errorf("appleScriptString = %q", got)
	}
}
