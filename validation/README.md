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
| 4 | `--behind-proxy` | a reverse proxy with a certificate (Caddy) | 30 min |
| 5 | `serve` (A2A server) | the command line of the official A2A SDK in JavaScript | 20 min |
| 6 | `remote` (A2A client) | the sample agent of the official A2A SDK in JavaScript | 30 min |
| 7 | `remote`, a task that takes time and a caller that gives up | the cancellable agent of the same SDK | 30 min |
| 8 | `serve --mcp` (MCP over HTTP) | the client of the official SDK of MCP in TypeScript, and the command line of the MCP Inspector | 20 min |
| 9 | `serve` (A2A server) | the client of the official A2A SDK in Python | 15 min |
| 10 | tool servers (MCP client), E1, E2, L7 | a server in Python that `uvx` starts | 10 min |
| 11 | the City Briefing sample: `think` with tools, `serve` and `remote` together | the Claude API and the fetch server that `uvx` starts | 20 min |

## 1. A client of MCP that is not ours

What the tests cannot show: how a real client starts the program (a minimal environment, its own
working folder), the order of its first messages, and how it shows the names of the tools.

The checks use the program at `~/bin/metagente`: the one of a release (see "Installing" in the README of
the project; copy the `metagente` of the archive to `~/bin`), or one built from the sources:

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
`~/Library/Application Support/Claude/claude_desktop_config.json`, which Settings → Developer → Edit
Config opens; it does not exist before the first server is added; keep what is already in it), with
**full paths**, because the client starts the program without the `PATH` of the shell and in a folder of
its own:

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

### With Claude Code instead

Claude Code, in a terminal, is another client of MCP that starts the program with `--stdio`:

```text
claude mcp add hello -- ~/bin/metagente serve --stdio \
  --config ~/metagente-demo/metagente.toml ~/metagente-demo/hello.ag
claude                                               # then ask it to greet Maria with Hello__greet
```

### Over HTTP (`--mcp`), with Claude Desktop

The custom connectors of Claude Desktop are reached from the servers of Anthropic, which do not reach
`127.0.0.1`, and they take OAuth, which `serve` does not offer. On this computer Claude Desktop reaches a
server over HTTP through a bridge that it starts itself, `mcp-remote`, which is of another team:

```text
cd ~/metagente-demo
umask 077; ~/bin/metagente token > .token
export METAGENTE_TOKEN=$(cat .token)
~/bin/metagente serve hello.ag --mcp 2>&1 | tee serve.log
```

and in the configuration of Claude Desktop, with the full path of a Node of version 20 or newer and the
token written in it (only for this test; take it out at the end):

```json
"hello-http": {
  "command": "/full/path/of/node/bin/npx",
  "args": ["-y", "mcp-remote@0.14.3", "http://127.0.0.1:8080/mcp",
           "--header", "Authorization:${AUTH_HEADER}"],
  "env": { "AUTH_HEADER": "Bearer THE-TOKEN", "PATH": "/full/path/of/node/bin:/usr/bin:/bin" }
}
```

Turn the `hello` of `--stdio` off in the conversation, so that the answer can only come over HTTP, and ask
again. **Expected:** `Hello, Maria!`, and in `serve.log` the six `GET` of `mcp-remote` looking for OAuth
(`401`, not counted: F11), `GET /mcp` with `405` (the server offers no stream of events), and `POST /mcp`
with `200` and `202`.

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
`HOME`, `USER`, `LANG` and `TMPDIR` are the ones that we pass; `npx` and the shell it uses add their own
(`npm_*`, `NODE`, `INIT_CWD`, `COLOR`, `EDITOR`, `SHLVL`, `_`, and on macOS `__CF_USER_TEXT_ENCODING`),
which `ChildEnv` could not pass because they are not in its list. `SECRET_TEST`, `ANTHROPIC_API_KEY`, `SSH_AUTH_SOCK` or any other variable of
your shell must **not** be there.

The package is not pinned (that is the warning of `check`), so what `npx` fetches changes with the
time. The tool that prints the environment was called `printEnv` and is `get-env` in the version
that was tried; if the server says that it has no such action, the message lists the ones it has.

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
date -u '+%H:%M:%S'; metagente run thinker.ag ask "question=What time is it now? Use the clock tool."; date -u '+%H:%M:%S'
```

**Expected:** the first answers with `4`. In the second, the clock gives the time in UTC, so the answer
has to be a time that falls between the two lines of `date -u` (and the date of UTC, which can be the day
after yours): that is what tells a call to the clock from an hour that the model made up. A small model
may not ask for the clock, and that is a fact about the model.
What matters to us is whether there was **no error of format**: a request refused by the server, an
answer that we could not read, a tool call that did not run. Keep the whole output of both.

## 4. A reverse proxy, with a certificate that is checked

What the tests cannot show: that a real proxy, with a real chain of certificates, reaches the server
the way the plan says: the server answers only to the names it was told, the Agent Card says the
address of the proxy and not the one in the request, and going around the proxy does not work.

The proxy is Caddy. It makes the certificate with a local authority of its own, so nothing has to be
bought or installed. The `Caddyfile` of the folder `validation/proxy` tells it not to touch the
keychain of the system, and the client trusts that authority call by call.

```text
brew install caddy                                     # once
```

Three terminals. In **A**, the server (the demo of check 1; the token goes to a file only for this
test, and is removed at the end):

```text
cd ~/metagente-demo
umask 077; ~/bin/metagente token > .token
export METAGENTE_TOKEN=$(cat .token)
~/bin/metagente serve hello.ag --behind-proxy --host hello.localhost:8443 --public-url https://hello.localhost:8443
```

In **B**, the proxy. Leave it running:

```text
cd validation/proxy                                    # from the root of the project
caddy run --config Caddyfile
```

In **C**, the client. `c` is `curl` told to trust the authority of Caddy, and to find `hello.localhost`
on this computer (on Linux the root is `~/.local/share/caddy/pki/authorities/local/root.crt`):

```text
export METAGENTE_TOKEN=$(cat ~/metagente-demo/.token)
c() { curl -sS --cacert "$HOME/Library/Application Support/Caddy/pki/authorities/local/root.crt" --resolve hello.localhost:8443:127.0.0.1 "$@"; }
CARD=https://hello.localhost:8443/agents/Hello/.well-known/agent-card.json
```

Then the five calls, one line each (the `echo` names each answer; do not put a `#` comment at the end of a
line when you paste into `zsh`, which does not read it as a comment and hands it to `curl`):

```text
echo "== 1"; c -H "Authorization: Bearer $METAGENTE_TOKEN" $CARD
echo; echo "== 2"; c -o /dev/null -w '%{http_code}\n' $CARD
echo "== 3"; c -o /dev/null -w '%{http_code}\n' -H "Origin: https://evil.example" -H "Authorization: Bearer $METAGENTE_TOKEN" $CARD
echo "== 4"; c -H "Authorization: Bearer $METAGENTE_TOKEN" -H 'Content-Type: application/json' -d '{"jsonrpc":"2.0","id":1,"method":"SendMessage","params":{"message":{"messageId":"m1","role":"ROLE_USER","parts":[{"data":{"skill":"greet","arguments":{"name":"Maria"}}}]}}}' https://hello.localhost:8443/agents/Hello
echo; echo "== 5"; curl -s -i -H "Authorization: Bearer $METAGENTE_TOKEN" http://127.0.0.1:8080/agents/Hello/.well-known/agent-card.json | head -12
```

**Expected:**

1. The card, with no error of certificate (there is no `-k`), and in it the address
   `https://hello.localhost:8443/agents/Hello`: it comes from `--public-url`, never from the request.
2. `401`.
3. `403`: a request that looks like it was made by a page in a browser is refused.
4. An answer of the JSON-RPC with `Hello, Maria!`: the body of a POST goes through the proxy.
5. `421` and `unexpected host`: the server was told that it answers to `hello.localhost:8443`, so
   going around the proxy, with the name `127.0.0.1:8080`, does not work.

Then look at the terminal **A**: there is one line for each request, with the agent, the method and the
result. Keep those lines. If **every** request through the proxy gets `421`, the proxy changed the
header `Host`: keep the output of `c -v ... $CARD`.

To end: `Ctrl-C` in A and in B, and `rm ~/metagente-demo/.token`. Caddy keeps its local authority in
its data folder; nothing was installed in the system.

## 5. A client of A2A that is not ours

What the tests cannot show: that a client written by the people who wrote the protocol, in another
language, finds the Agent Card of our server, understands it, and talks to it. Until now the server
was only tried with the Go SDK.

The client is the command line that comes with the official SDK in JavaScript (`a2aproject/a2a-js`,
version 1.0 of the protocol). It sends the token with `--auth`, also when it fetches the card, which is
where our server asks for it. It sends the message as **text**; our server gives that text to the one
value of a skill that takes only one, so for the `Hello` agent (a skill, `greet`, with a value, `name`)
there is no JSON to write.

In **A**, the server, as in check 1, on this computer and with no proxy:

```text
cd ~/metagente-demo
umask 077; ~/bin/metagente token > .token
export METAGENTE_TOKEN=$(cat .token)
~/bin/metagente serve hello.ag
```

In **B**, the client. It needs Node 20 or newer (`nvm use 22`), and it installs the dependencies of the SDK
with npm, so do it in a folder of its own, outside of this project:

```text
nvm use 22
git clone https://github.com/a2aproject/a2a-js ~/a2a-js
cd ~/a2a-js
npm install --no-save --ignore-scripts @grpc/grpc-js @bufbuild/protobuf
ls node_modules/@grpc node_modules/@bufbuild       # has to list grpc-js and protobuf
cd src/samples
export METAGENTE_TOKEN=$(cat ~/metagente-demo/.token)
npx tsx ./cli.ts http://127.0.0.1:8080/agents/Hello/ --transport JSONRPC --auth "Bearer $METAGENTE_TOKEN"
```

The install is made in the root of the clone and not in `src/samples`: the command line imports the
transport of gRPC, which is in `src/` of the SDK, and Node looks for its packages from there upwards. The two
packages are peer dependencies that the README of the SDK asks to install for gRPC, and npm does not
install them by itself. Without them the client stops with `Cannot find package '@grpc/grpc-js'`, before it
has talked to the server.

`--ignore-scripts` does not run the scripts of installation of the packages. A plain `npm install` in the root
of the clone failed here because `sharp` (a library of images that the SDK uses to be developed) tried to
build from source, and it is not needed to run the client. Not running the scripts of other people's
packages is also the right care for something that is installed only to be tried. Do **not** add
`--omit=dev`: the repository lists the two packages of gRPC as development dependencies, and with that
option npm leaves them out even when they are named in the command (only `jose` is installed, and the
client stops with the same `Cannot find package`). If the full install fails, the two packages alone can
go where Node looks for them, with
`npm install --prefix ~/a2a-js/src --no-save --no-package-lock --ignore-scripts @grpc/grpc-js @bufbuild/protobuf`.

(`npm run a2a:cli -- URL --auth ...` is the same command. The address is the one of the agent, not of the
server, and **ends with a `/`**: the client adds `.well-known/agent-card.json` to it the way a browser resolves a
relative address, so without the bar it takes `Hello` out and asks for `/agents/.well-known/agent-card.json`.)

**Expected:** the client prints the card it found (name `Hello`, the description, the version, `Streaming: Not
Supported`, the transport `JSONRPC`) and `Connected via JsonRpcTransport`, then waits at a prompt. Type
`Maria` and press Enter: the answer has `Hello, Maria!`. `/exit` ends it.

Then once more **without** `--auth`, to see the refusal from the side of the client:

```text
npx tsx ./cli.ts http://127.0.0.1:8080/agents/Hello/ --transport JSONRPC
```

**Expected:** an error while the card is fetched, and in it the status `401`.

Keep all that the client prints, and the lines that the server wrote in **A**. To end: `/exit` in B, `Ctrl-C`
in A, `rm ~/metagente-demo/.token`, and `rm -rf ~/a2a-js` if you do not want to keep the SDK.

## 6. A server of A2A that is not ours

What the tests cannot show: that our client, the `remote` declaration, talks to an agent written by the
people who wrote the protocol. Until now it was only tried with servers made with the Go SDK, and with our own.

The agent is `agents/sample-agent` of the SDK in JavaScript: a minimal agent of streaming that goes through the
life of a task (`submitted`, `working`, an artifact, `completed`). The command to start it is in the README of the
SDK; its **port** and the **id of its skill** are not in anything that could be read before, so this check reads
them from the agent and from its card.

In **A**, the agent (the install of check 5 is enough; `npm install` here is only to be sure):

```text
nvm use 22
cd ~/a2a-js/src/samples
npm install
npm run agents:sample-agent
```

Leave it running and write down the address that it prints. (Other examples of the protocol use the port 41241; it
is not confirmed for this one. Use what the agent printed.)

In **B**, the card and the baseline. First what the agent says to a text, with the client that is already known to work, so
that an answer that looks strange can be told from one that is the agent's own:

```text
export PORT=41241
curl -s http://localhost:$PORT/.well-known/agent-card.json | python3 -m json.tool | head -60
cd ~/a2a-js/src/samples
npx tsx ./cli.ts http://localhost:$PORT/ --transport JSONRPC
```

(`PORT` is the one that A printed. In the client, type `hello`, and keep the answer; then `/exit`.) In the card, note
`supportedInterfaces` (the address of the binding `JSONRPC`), `capabilities.streaming`, and the **`id` of each skill**.

In **C**, our client. The skill that is called is one of the ids of the card (if its id has characters that a name
cannot have, such as a dot or a space, write that down: the `.ag` cannot name it):

```text
export PORT=41241
export SKILL=THE-ID-OF-THE-SKILL
: "${PORT:?PORT is empty}" "${SKILL:?SKILL is empty}"
mkdir -p ~/metagente-demo/remote && cd ~/metagente-demo/remote
~/bin/metagente new caller
cat > caller.ag <<EOF
agent Caller
  goal "Ask the sample agent of the A2A SDK in JavaScript"
  remote Sample at "http://localhost:$PORT"
  accepts ask text
  on ask
    reply Sample.$SKILL text: text
EOF
~/bin/metagente check caller.ag
~/bin/metagente trust caller.ag
~/bin/metagente run caller.ag ask text=hello
```

`trust` lists the address of the agent as NEW; answer `y`. Run it **alone**: it asks, and a line pasted after it is read as the answer. The line with `: "${PORT:?...}"` stops the script if a variable is empty (writing `export sample_agent` instead of `export SKILL=sample_agent` leaves `SKILL` empty, and the line of the `.ag` comes out as `reply Sample. text: text`).

**Expected:** the call ends, it does not hang, with the text that the agent put in the artifact of the task: `Hello World! Nice to meet you!`. That shows that `SendMessage` is understood, that our client reads a task that is already completed (the agent answers a call that is not of streaming with one task) and the artifact, and that it sends **text** to an agent whose card says that it takes only text (`defaultInputModes: ["text"]`): the one value of the call, or a line `name: value` for each of several. To an agent that takes JSON, or says nothing, it sends the block of data `{"skill": ..., "arguments": ...}`, which is what our own agents read. Before F8 was fixed the client sent the data to this agent too, and it answered `Hello! Please provide a message for me to respond to.`: it had not read the data. If the answer is odd, the call by hand tells whether it is the agent or the client (the address is the one of `supportedInterfaces`):

```text
curl -s -X POST ADDRESS-OF-THE-BINDING -H 'Content-Type: application/json' -H 'A2A-Version: 1.0' \
  -d '{"jsonrpc":"2.0","id":1,"method":"SendMessage","params":{"message":{"messageId":"m1","role":"ROLE_USER","parts":[{"text":"hello"}]}}}'
```

Keep: the card (the part with the skills and the address), what the official client got for `hello`, everything
`run` printed, the call by hand if you made it, and what A wrote. To end: `Ctrl-C` in A.

## 7. A task that takes time, and a caller that gives up

What the tests cannot show: what our client does with a slow agent of a third party, and, above all, whether the
agent is told when whoever called gives up. The sample is `agents/cancellable-agent` of the SDK in JavaScript. According
to its README it runs a task of five steps of one second and checks, before each step, whether the task was cancelled. In
the terminal of the agent, a cancellation that arrived looks like `Cancellation requested for task <id>` and
`Aborting task <id> at step N`.

Two things that this check is not: it does not follow a task with `GetTask`, because a call that is not of streaming waits
for the task to end and gets it finished, as in check 6; and the cancelling client of the sample is only used to see what a
cancellation looks like.

In **A**, the agent. Stop the sample agent of check 6 first, if it is running: both use the port 41241.

```text
nvm use 22
cd ~/a2a-js/src/samples
npm run agents:cancellable-agent
```

In **B**, the card, and the baseline: the client of the sample cancels its own task after 2.5 seconds.

```text
export PORT=41241
curl -s http://localhost:$PORT/.well-known/agent-card.json | python3 -m json.tool | head -60
cd ~/a2a-js/src/samples
npm run agents:cancellable-client
```

Look at the terminal **A**: the lines of the steps, `Cancellation requested for task ...` and `Aborting task ...`. That is what a
cancellation that arrives looks like. Note the `id` of the skill in the card. In **C**, our client, with two messages: one
that waits, and one that gives up after two seconds (the value of the skill is only a text, which this agent does not read):

```text
export PORT=41241
export SKILL=THE-ID-OF-THE-SKILL
: "${PORT:?PORT is empty}" "${SKILL:?SKILL is empty}"
mkdir -p ~/metagente-demo/slow && cd ~/metagente-demo/slow
~/bin/metagente new slow
cat > slow.ag <<EOF
agent Slow
  goal "Call the slow agent of the A2A SDK in JavaScript"
  remote Worker at "http://localhost:$PORT"
  accepts ask text
  accepts hurry text
  on ask
    reply Worker.$SKILL text: text
  on hurry
    reply Worker.$SKILL text: text within 2 seconds
EOF
~/bin/metagente check slow.ag
~/bin/metagente trust slow.ag
time ~/bin/metagente run slow.ag ask text=hello
~/bin/metagente run slow.ag hurry text=hello
```

`trust` lists the address as NEW; answer `y`, and run it alone.

**Expected for `ask`:** it ends after about five seconds with the text of the artifact, and the terminal A shows the
five steps. **Expected for `hurry`:** it ends after about two seconds with `did not finish within 2 seconds`. What this check
is for is what the terminal **A** shows next: either `Cancellation requested for task ...` and `Aborting task ...`, which means that our
client told the agent that nobody waits any more, or the steps going on to `5/5`, which means that it did not. In the second case
the task goes on working for nothing. To see how the task of the agent ended, wait six seconds and ask for it by hand (the
`id` is the one in the lines of the terminal A):

```text
curl -s -X POST http://localhost:$PORT/ -H 'Content-Type: application/json' -H 'A2A-Version: 1.0' \
  -d '{"jsonrpc":"2.0","id":1,"method":"GetTask","params":{"id":"THE-ID-OF-THE-TASK"}}' | python3 -m json.tool | head -30
```

`TASK_STATE_CANCELED` means that the agent was told; `TASK_STATE_COMPLETED` means that it was not. Keep: the card, what
the sample client and the agent wrote in the baseline, what each `run` printed and how long `ask` took, what A wrote during
`ask` and during `hurry`, and the answer of `GetTask`. To end: `Ctrl-C` in A.

## 8. A client of MCP over HTTP that is not ours

What the tests cannot show: that a client of another team, in another language, gets through the door
(the fetch of Node.js, the headers of the streamable HTTP of MCP), keeps the session the server issued,
and ends it.

In **A**, the server, with two agents: the `Hello` of check 1 and `notes.ag` of `validation/mcp-http`,
which remembers one thing (the token goes to a file only for this test):

```text
cd ~/metagente-demo
cp PROJECT/validation/mcp-http/notes.ag .            # PROJECT is the root of the project
~/bin/metagente trust notes.ag
umask 077; ~/bin/metagente token > .token
export METAGENTE_TOKEN=$(cat .token)
~/bin/metagente serve hello.ag notes.ag --mcp
```

The banner has a line `MCP ... /mcp (the agents as MCP tools)`.

In **B**, the client of the official SDK in TypeScript (Node 20 or newer), in a folder of its own,
with the script `client.mjs` of `validation/mcp-http`:

```text
mkdir -p ~/mcp-http-check && cd ~/mcp-http-check
cp PROJECT/validation/mcp-http/client.mjs .
npm install --ignore-scripts --no-audit --no-fund @modelcontextprotocol/sdk
export METAGENTE_TOKEN=$(cat ~/metagente-demo/.token)
node client.mjs http://127.0.0.1:8080/mcp
```

**Expected:** a session issued; the three tools; `Hello, Maria!`; `ok`, then `Ana` in the same session and
an empty text in another one; a missing value refused with `Problem` and `Fix` and `isError`; the session
ended; and, without the token, `unauthorized`.

Then the command line of the Inspector, in the same folder:

```text
npm install --ignore-scripts --no-audit --no-fund @modelcontextprotocol/inspector
npx --no-install mcp-inspector --cli http://127.0.0.1:8080/mcp --transport http --header "Authorization: Bearer $METAGENTE_TOKEN" --method tools/list
npx --no-install mcp-inspector --cli http://127.0.0.1:8080/mcp --transport http --header "Authorization: Bearer $METAGENTE_TOKEN" --method tools/call --tool-name Hello__greet --tool-arg name=Maria
```

**Expected:** the three tools, and `Hello, Maria!`. Without `--header` the Inspector tries OAuth and stops:
the server asks for a bearer token and offers no OAuth.

Keep what B printed and the lines of the access log in A (`rpc=mcp message=...`). To end: `Ctrl-C` in A,
`rm ~/metagente-demo/.token`, and `rm -rf ~/mcp-http-check`.

## 9. A client of A2A in Python

What the tests cannot show: that the official client in Python, a third language after Go and JavaScript,
reads the card and the answers, and tells our errors apart. The original project ran the same SDK.

In **A**, the server, with an agent that answers and one message that fails:

```text
mkdir -p ~/metagente-demo/python && cd ~/metagente-demo/python
cat > weather.ag <<'EOF'
agent Weather
  goal "Answer questions about the weather"
  accepts ask city  # weather for a city
  accepts broken
  on ask
    reply "sunny in {city}"
  on broken
    fail "the barometer exploded"
EOF
umask 077; ~/bin/metagente token > .token
export METAGENTE_TOKEN=$(cat .token)
~/bin/metagente serve weather.ag
```

In **B**, the client (Python 3.10 or newer), in an environment of its own:

```text
python3 -m venv ~/a2a-python && ~/a2a-python/bin/pip install a2a-sdk httpx
export METAGENTE_TOKEN=$(cat ~/metagente-demo/python/.token)
~/a2a-python/bin/python PROJECT/validation/a2a-python/client.py http://127.0.0.1:8080/agents/Weather/
```

**Expected:** the name `Weather`, the skills `ask` and `broken`, the interface `JSONRPC` `1.0`; `sunny in
Lisbon` as a message; `TASK_STATE_FAILED` with `the barometer exploded`; `GetTask` raising
`TaskNotFoundError`; a skill that is not there raising `InvalidParamsError` with the skills that are; and,
without the token, `401`. To end: `Ctrl-C` in A, `rm -rf ~/a2a-python ~/metagente-demo/python`.

## 10. A tool server that `uvx` starts

What the tests cannot show: that `uvx` finds what it needs in the minimal environment of E1 (it keeps its
packages in the folder of the user, so it needs `HOME`), that a server written in Python with its SDK
talks to our client, and what `readonly` does with a server of another team. The server is
`mcp-server-fetch`, the one of the City Briefing sample, and the agent calls it directly: no model and
no key.

```text
mkdir -p ~/metagente-demo/uvx && cd ~/metagente-demo/uvx
cp PROJECT/validation/uvx/probe.ag .
uvx mcp-server-fetch==2026.8.18 --help         # the first time, uvx fetches the package (about 10 s)
export SECRET_TEST=must-not-appear
~/bin/metagente check --strict probe.ag        # no problems: the version is pinned
~/bin/metagente trust probe.ag                 # NEW: starts the program uvx mcp-server-fetch==2026.8.18; answer y
~/bin/metagente run probe.ag get url=https://example.com
pgrep -fl mcp-server-fetch || echo "no fetch server left"
```

**Expected:** `run` prints `Contents of https://example.com/:` and the text of the page (the fetch server
keeps the body of a page, not its title), and nothing of the server is left running after it ends.

Then two variants, in the same folder:

```text
sed 's|2026.8.18"|2026.8.18" readonly|' probe.ag > readonly.ag
~/bin/metagente run readonly.ag get url=https://example.com
sed 's|==2026.8.18||' probe.ag > unpinned.ag
~/bin/metagente check unpinned.ag
~/bin/metagente check --strict unpinned.ag
```

**Expected:** `readonly.ag` is refused with "`fetch.fetch` is not available because `tool fetch` was declared
readonly, and the tool server does not mark it as read only": the fetch server marks none of its tools as
read only, so `readonly` leaves nothing to call. (It is approved already: the approval is of the command,
which did not change.) `unpinned.ag` gets a warning that the version is not pinned, and `--strict` refuses it.

Keep what each command printed. To end: `rm -rf ~/metagente-demo/uvx`.

## 11. The City Briefing sample, with Claude

What the tests cannot show: the sample run as its README says, with the Claude API in place of the scripted
model and `mcp-server-fetch` in place of the fake one. The model chooses to call the fetch tool, reads a page
of Wikipedia, and the Concierge gets the facts over A2A with the token. It needs an Anthropic API key, and the
use is billed (a few short questions); check 10 first, so that `uvx` is known to work.

The files come from the project, into a folder of their own, so that the approvals and the token are not in
the project:

```text
mkdir -p ~/metagente-demo/city-briefing && cd ~/metagente-demo/city-briefing
for f in researcher.ag concierge.ag metagente.toml; do
  gh api -H "Accept: application/vnd.github.raw" repos/Ribeiro/metagente-go/contents/samples/city-briefing/$f > $f
done
~/bin/metagente check researcher.ag && ~/bin/metagente check concierge.ag
~/bin/metagente trust researcher.ag                # NEW: starts the program uvx mcp-server-fetch==2026.8.18
~/bin/metagente trust concierge.ag                 # NEW: connects to http://127.0.0.1:8080/agents/Researcher (...)
umask 077; ~/bin/metagente token > .researcher-token
```

In both terminals, set the key without showing it, and check it before the agents use it:

```text
export ANTHROPIC_API_KEY=$(cat FILE-WITH-THE-KEY)   # only the key, one line
curl -s -o /dev/null -w '%{http_code}\n' https://api.anthropic.com/v1/models \
  -H "x-api-key: $ANTHROPIC_API_KEY" -H "anthropic-version: 2023-06-01"     # 200
```

Terminal 1:

```text
~/bin/metagente run researcher.ag city=Lisbon
export METAGENTE_TOKEN=$(cat .researcher-token)
~/bin/metagente serve researcher.ag
```

Terminal 2:

```text
export RESEARCHER_TOKEN=$(cat .researcher-token)
~/bin/metagente run concierge.ag city=Lisbon
~/bin/metagente run concierge.ag city=Xyzzyplugh
curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:8080/agents/Researcher/.well-known/agent-card.json
```

**Expected:** the Researcher alone prints three sentences of facts and `(checked ...)`. The Concierge prints a
briefing of three lines about Lisbon, and for `Xyzzyplugh` says that no facts were found. The `curl` without
the token gets `401`. The log of Terminal 1 has, for each run of the Concierge, a `GET` of the card and a
`SendMessage` with `result=ok`. The key is in no output and no log.

Keep what each command printed, without the key. To end: Ctrl-C in Terminal 1, and
`rm -rf ~/metagente-demo/city-briefing`.

## Results

| # | Date | System | Version (`metagente --version`) | Result | Notes |
|---|---|---|---|---|---|
| 1 | 2026-10-04 | macOS, arm64 | 0.0.0-dev | passed with the MCP Inspector 1.0.2 | `initialize`, `tools/list` and `tools/call` work; the answer was `Hello, Maria!`. Not tried with a desktop assistant. |
| 1 | 2026-10-06 | macOS, arm64; Claude Desktop and Claude Code (versions not recorded) | 0.3.1 | passed | Claude Desktop, with the configuration above in a file that did not exist before, listed the tool and answered `Hello, Maria!`; Claude Code, with `claude mcp add`, did the same. Both start the program themselves, with `--stdio` and full paths. |
| 1 | 2026-10-06 | macOS, arm64; Claude Desktop with `mcp-remote` 0.14.3 (Node of `nvm`), over HTTP | 0.3.1 | passed, after F11 | The first try ended in `Server disconnected`: the bridge got `429` with the right token, because its requests looking for OAuth had counted as wrong tokens (F11). Started again after the minute, the server got, from two copies of the bridge, twelve `GET` of OAuth (`401`), eight `GET /mcp` (`405`) and `POST /mcp` with `200` and `202`; with the `hello` of `--stdio` turned off in the conversation, Claude Desktop answered `Hello, Maria!` over HTTP. The fix of F11 is in the code after 0.3.1. |
| 2 | 2026-10-04 | macOS, arm64; Node 18.18.2, npm 9.8.1 | 0.0.0-dev | passed | `npx` started `@modelcontextprotocol/server-everything` (not pinned; its version was not recorded) with the minimal environment. `say` answered `Echo: hello`. In `get-env`, `SECRET_TEST` and `ANTHROPIC_API_KEY` did not appear, nor did any other variable of the shell. The tool is `get-env` in the version fetched, not `printEnv`. The server does receive the whole `PATH` and the `HOME`: the environment is minimal, not an isolation. |
| 3 | 2026-10-04 | macOS, arm64; Ollama 0.30.11, `llama3.1` (8B) | 0.0.0-dev | passed, with reservations | The request is accepted and the answers are read. With "What time is it now? Use the clock tool." the model asked for `clock__now`, the program ran it, and the answer had the time and the date of UTC, inside the two readings of `date -u`. Other ways to ask failed: an hour that was made up (`23:35`, which is neither UTC nor local), and a call written as text (`{"name": "clock", ...}`) followed by an invented result. A model of 8B is not reliable at this, and the program cannot tell, so it gives that text as the answer. |
| 4 | 2026-10-04 | macOS, arm64; Caddy (version not recorded) | 0.0.0-dev | passed | Through a reverse proxy with the certificate of the local authority of Caddy, checked without `-k`: the card says `https://hello.localhost:8443/agents/Hello`; no token gives `401`; a header `Origin` gives `403`; a `SendMessage` through the proxy answers `Hello, Maria!`; going around the proxy, to `127.0.0.1:8080`, gives `421 unexpected host`. The host of the request and the host of the public address were the same, so the card does not show that the address is not taken from the request: that is what the test `TestTheCardDoesNotChangeWithTheHostOfTheRequest` checks. The access log has one line for each request, as it should: the call to the agent with `agent=Hello rpc=SendMessage message=greet task=... result=ok`, the refusals without those fields, the sizes of the answers right, and no token, no body and no value in any of them. |
| 5 | 2026-10-04 | macOS, arm64; Node v22.17.0; `a2aproject/a2a-js` (the command line of the official SDK, protocol 1.0) | 0.0.0-dev | passed, after F6 | The card is found and read (name, description, version, the transport `JSONRPC` from `supportedInterfaces`); the client uses `sendMessageStream`, the card says that there is no streaming, and it falls back to one answer; a text typed as `Maria` became `Hello, Maria!` (a new session, `Ana`, gave `Hello, Ana!`), and the server gave the client a `contextId`. Without `--auth` the card is refused with `401`. The first attempt was refused with `403`: the fetch of Node.js sends `Sec-Fetch-Mode: cors`, and the rule of S4 took it for a browser (F6). A second message in the same session (`Beto`) was answered with `Hello, Beto!`, and the client did not print `Context ID updated`, which it does only when the server gives it another identifier: the conversation went on. On the side of the server this is tested in `TestARealAgentRemembersInItsConversationAndForgetsWhenItEnds`. |
| 6 | 2026-10-05 | macOS, arm64; `agents/sample-agent` of `a2aproject/a2a-js` (`npm run agents:sample-agent`), port 41241 | 0.0.0-dev | passed, after F8 | The card is read (the address of the binding `JSONRPC`, the skill `sample_agent`, `streaming: true`, input only `text`) and its address is approved. The first call answered `Hello! Please provide a message for me to respond to.`: our client sent a block of data, and the agent reads only text (a call by hand with text answered `Hello World! Nice to meet you!`, and one with data gave the fallback). After the fix the client sends text to an agent whose card takes only text, and the answer was `Hello World! Nice to meet you!`. The agent answers a call that is not of streaming with a task that is already completed, so following a task that is still working (`GetTask`) and cancelling it were not tried with it, and neither was a call with several values. |
| 7 | 2026-10-05 | macOS, arm64; `agents/cancellable-agent` of `a2aproject/a2a-js`, port 41241 | 0.0.0-dev | passed, after F9 | `ask` waited 5.1 s, as the agent runs five steps of one second. It printed nothing, and that is right: the task ended `COMPLETED` with no artifact and no message in its status, so there was nothing to print. `hurry` (`within 2 seconds`) failed after 2 s, as it should, but the agent went on and ran the five steps to the end: its log shows no `Cancellation requested`, and `GetTask` says `TASK_STATE_COMPLETED`, not `CANCELED`. After the fix (F9): `hurry` made the agent write `Cancellation requested for task ...` and `Aborting task ... at step 3`, and `GetTask` of that task says `TASK_STATE_CANCELED`; `ask` took 5.2 s, now by following the task with `GetTask` until it ended, and printed nothing, as before. |
| 8 | 2026-10-05 | Linux, amd64; Node v22.22.0; `@modelcontextprotocol/sdk` 1.32.1; `@modelcontextprotocol/inspector` 2.9.0 | 0.0.0-dev | passed | The client of the SDK in TypeScript got a session id from the server, listed `Hello__greet`, `Notes__recall` and `Notes__remember`, got `Hello, Maria!`, and `Notes` remembered `Ana` in its session and nothing in another one; a missing value came back as an error of the tool with `Problem` and `Fix`; `terminateSession` (a `DELETE`) worked; without the token it was refused (`unauthorized`). The fetch of Node.js went through the door (F6 holds). The Inspector listed the tools and called `Hello__greet` (`Hello, Maria!`); with `--strict` it warns that the schema of each value says nothing of its type (F3). By hand: an `Origin` gets `403`, a `GET` gets `405` with `Allow: POST, DELETE`. The access log has `agent=Hello rpc=mcp message=greet task=... result=ok`, and no token. Not tried: behind a proxy, and with a desktop assistant. |
| 9 | 2026-10-05 | Linux, amd64; Python 3.11.15; `a2a-sdk` 1.2.2 | 0.0.0-dev (the `master` of 0.2.0 with the changes of the port of the tests) | passed | The card was read with the token (name, the skills `ask` and `broken`, `JSONRPC` `1.0`). `ask` with the text `Lisbon` and the skill in the metadata came back as a message, `sunny in Lisbon`; `broken` as a task in `TASK_STATE_FAILED` with `the barometer exploded`. The SDK turned our errors into its own types: `GetTask` raised `TaskNotFoundError` and a skill that is not there `InvalidParamsError: this agent does not handle `dance`; it handles: ask, broken`. Without the token the card was refused with `401`. The access log had one line for each request, with `result=ok`, `failed` and `error`. |
| 10 | 2026-10-05 | macOS, arm64 (uv, version not recorded), and Linux, amd64 (uv 0.8.17); `mcp-server-fetch` 2026.8.18 | 0.3.0 on macOS; 0.0.0-dev (the `master` of 0.3.0) on Linux | passed | On macOS `check --strict` found no problems with the version pinned, `trust` listed `starts the program: uvx mcp-server-fetch==2026.8.18` as NEW, and `run` printed `Contents of https://example.com/:` with the text of the page; no process of the server was left. With `readonly` the call was refused, because the server marks none of its tools as read only. Without a version `check` gave the warning and passed, and `--strict` refused it. On Linux the same was seen except the page, which the proxy of that environment does not let through (403); there `@2026.8.18` was pinned as well as `==2026.8.18`, and a failure of the server came back as a problem that can be read. A first try on macOS used an older program built from the sources (it said `0.0.0-dev`), and there the server stopped while answering; with the 0.3.0 of the release it did not happen, so it was not looked into: the checks are to be run with the program of a release, or one built from the `master` of the day. |
| 11 | 2026-10-05 | macOS, arm64; Claude Sonnet 5.5 (`claude-sonnet-5-5`); `mcp-server-fetch` 2026.8.18 | 0.3.0 | passed, after F10 | `check` found no problems in either file; `trust` listed `starts the program: uvx mcp-server-fetch==2026.8.18` for the Researcher and `connects to: http://127.0.0.1:8080/agents/Researcher (and sends it the token held in RESEARCHER_TOKEN)` for the Concierge. With `Lisbon` the Concierge printed three lines ("Welcome to Lisbon, Portugal's capital since 1256, set on the Tagus river!", its Roman name Olissipo and the siege of 1147, Belém Tower, Rua Augusta Arch and the cathedral). With `Xyzzyplugh` it said that no verified facts were found, because the page of Wikipedia answered 404. The log of the Researcher had a `GET` of the card (200) and a `SendMessage` with `result=ok` for each (7.3 s and 4.5 s), and a `curl` without the token got `401`. On the way: with the key not set to a real one, `api.anthropic.com answered 401: invalid x-api-key` came back to the Concierge over A2A as a problem that can be read, and the task was `result=failed` in the log. A variable that held a command along with a line break got "I could not reach api.anthropic.com: the connection failed", which sent the search to the network (F10). |

## What the checks found

Not defects of the tests, but things that a real use showed. "Open" means that nothing was changed yet.

| # | Found in | What | State |
|---|---|---|---|
| F1 | 3 | The approval of `think` says "sends your key and what the agent asks the language model to" even when the address is of this computer and there is no key. It says more than what happens. | fixed: when no key is set and the address is of this computer, the approval says "with no key (none is set)" |
| F2 | 3 | A small model can write a call to a tool as text and invent its result. The answer goes out as the answer of the agent. | open, a limit of the model |
| F3 | 1 | The schema of a tool does not say the type of the values, so a generic client (the Inspector) shows a JSON editor and a text has to be written between quotes. | open, a choice: the language has no types in the interface |
| F4 | 4 | Behind a proxy the banner shows only the public address (`https://hello.localhost:8443`), not the address where the server listens (`127.0.0.1:8080`), which is what the person who writes the proxy needs. | fixed: when the address of the banner is not where the server listens, a line says where, and for which Host |
| F5 | 4 | Behind a proxy every line of the access log has `remote=127.0.0.1`, the one that went around the proxy too, so it looks like the others unless the status (421) is read. Writing the header `Host` that was received (shortened with `clip.Collapse`, since it comes from outside) would tell the two apart. | fixed: every line of the log has `host=`, shortened and without control characters |
| F6 | 5 | The fetch of Node.js sends `Sec-Fetch-Mode: cors` in every request, and the rule of S4 (any `Sec-Fetch-*`) refused the official client in JavaScript with `403`. | fixed (S4): only `Origin`, `Sec-Fetch-Site`, `Sec-Fetch-Dest` and `Sec-Fetch-User` tell a browser, checked with the official client |
| F7 | CI | On Windows the tool of files does not detect hard links: `linkCount` (links_other.go) always says 1, so a write through a hard link that leads outside the folder is not refused there (F4). Making a hard link takes someone else; the agent has no tool for it. The test of this is skipped on Windows, with that reason in the message. | open; GetFileInformationByHandle in the standard `syscall` gives the number, but it needs the handle of the file |
| F8 | 6 | `remote` always sent the call as a block of data. The sample agent of the SDK in JavaScript takes only text (`defaultInputModes: ["text"]`), found no text in the message and answered `Please provide a message for me to respond to`. | fixed in the code: an agent whose card takes only text is sent text, the one value or a line `name: value` for each of several; one that takes JSON, or says nothing, is sent the data as before. checked with the sample agent |
| F9 | 7 | When the time of a call ends while the other side works, `remote` gave up without telling the agent, and the task went on to the end for nothing. The call asked the other side to answer only when the task ended (`returnImmediately: false`), so the number of the task was only known at the end, and there was nothing to cancel. | fixed in the code: it asks to be answered at once, follows the task with `GetTask` and cancels it when the caller gives up. checked with the cancellable agent |
| F10 | 11 | A key with a line break, a space or another character that a header cannot hold (more than the key copied into the variable) was refused by the HTTP library before any connection, and the person was told `I could not reach api.anthropic.com: the connection failed`, after three tries. | fixed in the code: such a key is refused before any request, with "the variable ANTHROPIC_API_KEY holds a line break, which cannot be part of a key" and how to fix it, and nothing of the key is said. The README of the sample has it in its troubleshooting |
| F11 | 1 (over HTTP) | Claude Desktop reaches a server of MCP over HTTP through the bridge `mcp-remote`, which looks for OAuth before it connects: six `GET` without a token (`/.well-known/oauth-protected-resource`, `/.well-known/oauth-authorization-server`, `/.well-known/openid-configuration` and their `/mcp` forms) each time it starts, and Claude Desktop starts more than one. Each `401` counted as a wrong token, so within a minute the server answered `429` to the bridge itself, with the right token, and Claude Desktop said `Server disconnected`. | fixed in the code: a request with no `Authorization` at all is refused and not counted; a wrong token, another scheme or an empty `Bearer` still counts (`TestARequestWithoutATokenIsRefusedButNotCounted`) |
