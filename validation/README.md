# Validation in real conditions

The tests of this project use fakes: a client of the SDK in memory, a server of the SDK, scripted
models. They show that the parts agree with each other, not that they agree with the programs that
other people wrote. Each check below puts one part in front of the real thing.

Do them in this order, each one is independent of the next. After each one, write the result in the
table at the end, and if it fails, keep the text of the error: it is what the fix starts from.

| # | Part | Real thing | Takes |
|---|---|---|---|
| 1 | `serve --stdio` (MCP server) | a desktop client that speaks MCP | 15 min |
| 2 | tool servers (MCP client), E1 | a server started by `npx` | 15 min |
| 3 | `think` with `openai-compatible` | a model that runs on this computer (Ollama) | 20 min |
| 4 | `--public` and `--behind-proxy` | a reverse proxy with a certificate | written after 1 to 3 |

## 1. A client of MCP that is not ours

What the tests cannot show: how a real client starts the program (a minimal environment, its own
working folder), the order of its first messages, and how it shows the names of the tools.

```text
mkdir -p ~/bin
go build -o ~/bin/metagente ./cmd/metagente          # from the root of the project
mkdir -p ~/metagente-demo && cd ~/metagente-demo
~/bin/metagente new hello                            # hello.ag and metagente.toml, here
~/bin/metagente trust hello.ag                       # approvals belong to this folder
~/bin/metagente serve hello.ag --stdio               # by hand first; Ctrl-D ends it
```

The last line has to print, on the error output, the tool `Hello__greet` and wait. Then, in the
settings of the client (Claude Desktop on macOS:
`~/Library/Application Support/Claude/claude_desktop_config.json`; keep what is already in the file),
with **full paths**:

```json
{
  "mcpServers": {
    "hello": {
      "command": "/Users/YOU/bin/metagente",
      "args": ["serve", "--stdio", "--config", "/Users/YOU/metagente-demo/metagente.toml",
               "/Users/YOU/metagente-demo/hello.ag"]
    }
  }
}
```

Quit the client completely and open it again. Ask it to use the tool `Hello__greet` with the name
Maria. **Expected:** the tool is listed, and the answer has `Hello, Maria!`.

If it does not work, the client keeps a log per server (Claude Desktop:
`~/Library/Logs/Claude/mcp-server-hello.log`). Keep its first 40 lines.

### With the MCP Inspector instead

The Inspector is the debugging tool of the MCP project itself, written with the SDK in TypeScript,
so it is a client from another team in another language. From the folder of the demo:

```text
cd ~/metagente-demo
npx @modelcontextprotocol/inspector ~/bin/metagente serve hello.ag --stdio
```

Open the address it prints, press **Connect**, then **Tools**, **List Tools**, choose the tool and
run it. Two things to know:

- Do not give our `--config` to it directly: the Inspector has an option of the same name, takes it
  for itself and fails with `Unexpected token '#', "# Settings"... is not valid JSON`. Run it from the
  folder of `metagente.toml`, as above, or separate the two with `--`.
- The schema of a tool does not say what type a value has (the interface of an agent names its
  values, not their types), so the Inspector shows a JSON editor for each one. A text goes **between
  quotes**: `"Maria"`. Without them, `{"name": "Maria"}` is an object, and the agent receives a record.

## 2. A tool server that is not ours, and the environment it receives (E1)

What the tests cannot show: that `npx` finds what it needs in the minimal environment, and that
nothing else of the person's environment reaches the server.

```text
cd validation/tool-server
npx -y @modelcontextprotocol/server-everything < /dev/null    # only fetches the package, ends at once
export SECRET_TEST=must-not-appear ANTHROPIC_API_KEY=not-a-real-key
metagente check probe.ag      # expected: a warning that the version of the package is not pinned (E2)
metagente trust probe.ag      # lists what it starts, as NEW; answer yes
metagente run probe.ag say text=hello
metagente run probe.ag show_env
```

**Expected:** `say` answers with the text echoed. `show_env` lists the variables of the server: `PATH`,
`HOME`, `USER` and a few more are right; the ones that `npx` itself sets (`npm_*`, `NODE`, `INIT_CWD`)
are not ours either. `SECRET_TEST`, `ANTHROPIC_API_KEY`, `SSH_AUTH_SOCK` or any other variable of
your shell must **not** be there. If the server says that it has no tool `printEnv`, this version
of it changed the name: keep the message.

## 3. A model that is not Anthropic's (Ollama)

What the tests cannot show: that a real server of the format of OpenAI accepts our request and that
we understand its answers, above all the requests for tools.

```text
brew install ollama                    # or the app of ollama.com
ollama pull llama3.1                   # a model that knows tools; qwen2.5 also does
curl http://localhost:11434/v1/models  # has to answer with JSON that lists the model
cd validation/ollama
metagente check thinker.ag
metagente trust thinker.ag             # the address of the model comes as NEW (T1 and T3)
metagente run thinker.ag ask "question=What is 2+2? Answer with the number only."
metagente run thinker.ag ask "question=What time is it now? Use the clock."
```

**Expected:** the first answers with `4`. The second shows whether the model asked for the clock and
whether the answer came out; a small model may not ask for it, and that is a fact about the model.
What matters to us is whether there was **no error of format**: a request refused by the server, an
answer that we could not read, a tool call that did not run. Keep the whole output of both.

## 4. A reverse proxy with a real certificate

Written after 1 to 3, because it depends on what they show about the options of `serve`.

## Results

| # | Date | System | Version (`metagente --version`) | Result | Notes |
|---|---|---|---|---|---|
| 1 | 2026-10-04 | macOS, arm64 | 0.0.0-dev | passed with the MCP Inspector 1.0.2 | `initialize`, `tools/list` and `tools/call` work; the answer was `Hello, Maria!`. Not tried with a desktop assistant. |
| 2 | | | | not run | |
| 3 | | | | not run | |
| 4 | | | | not run | |
