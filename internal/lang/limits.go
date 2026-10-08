package lang

// Limits that keep hostile or accidental input from exhausting time or memory
// (requirements D1 and D4).
const (
	// MaxSourceBytes is the largest .ag file the parser accepts.
	MaxSourceBytes = 1 << 20
	// MaxLineBytes is the longest line the lexer accepts.
	MaxLineBytes = 64 << 10
	// MaxIndentLevels is the deepest nesting the lexer accepts.
	MaxIndentLevels = 32
	// MaxListItems is the longest list literal the parser accepts.
	MaxListItems = 10000
	// MaxNumberDigits is the most digits a number literal may have.
	MaxNumberDigits = 15
	// MaxRetryAfter is the longest wait, in seconds, that `fail ... retry in N seconds` may suggest.
	MaxRetryAfter = 3600
	// MaxRepeatLimit is the largest N of `up to N times`. The run has its own ceiling (max_loop_turns).
	MaxRepeatLimit = 1000000000
)
