package lang

// ParamInfo describes one value an action takes.
type ParamInfo struct {
	Name     string
	Required bool
}

// ActionInfo describes one action of a built in tool. The checker uses it to
// catch mistakes before an agent runs.
type ActionInfo struct {
	Name        string
	Description string
	Params      []ParamInfo
	// Mutates is true for actions that change something. `readonly` removes
	// them (requirement L7).
	Mutates bool
	// Schema is the JSON schema of the values the action takes, when the tool
	// publishes one (tool servers do). It is what a language model is shown.
	Schema any
}

var builtinTable = map[ToolKind][]ActionInfo{
	ToolFile: {
		{Name: "read", Description: "Read a text file and return its content",
			Params: []ParamInfo{{"path", true}}},
		{Name: "write", Description: "Write text to a file",
			Params: []ParamInfo{{"path", true}, {"text", true}}, Mutates: true},
	},
	ToolHTTP: {
		{Name: "get", Description: "Fetch a web address",
			Params: []ParamInfo{{"url", true}}},
		{Name: "post", Description: "Send data to a web address",
			Params: []ParamInfo{{"url", true}, {"body", false}}, Mutates: true},
	},
	ToolEnv: {
		{Name: "get", Description: "Read an environment variable",
			Params: []ParamInfo{{"name", true}}},
	},
	ToolState: {
		{Name: "set", Description: "Remember a value",
			Params: []ParamInfo{{"key", true}, {"value", true}}, Mutates: true},
		{Name: "get", Description: "Recall a value",
			Params: []ParamInfo{{"key", true}}},
	},
	ToolBroker: {
		{Name: "publish", Description: "Publish a message to a subject of the broker, with an id that makes a copy recognizable",
			Params: []ParamInfo{{"subject", true}, {"id", true}, {"data", true}}, Mutates: true},
	},
	ToolClock: {
		{Name: "now", Description: "The current date and time"},
		{Name: "wait", Description: "Wait for a number of seconds",
			Params: []ParamInfo{{"seconds", true}}},
	},
}

// AllBuiltinActions lists every action of a kind of built in tool, whether or
// not a particular declaration allows it. MCP tools have none: their actions
// are only known when the server is running.
func AllBuiltinActions(kind ToolKind) []ActionInfo {
	return builtinTable[kind]
}

// BuiltinActions lists the actions a declared built in tool offers, taking
// `readonly` into account.
func BuiltinActions(tool *ToolDecl) []ActionInfo {
	var out []ActionInfo
	for _, action := range builtinTable[tool.Kind] {
		if tool.ReadOnly && action.Mutates {
			continue
		}
		out = append(out, action)
	}
	return out
}
