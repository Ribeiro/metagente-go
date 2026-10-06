# City Briefing: two agents, A2A and MCP

Ask about a city and get a short briefing. Two Metagente agents do it together:

```text
you --run--> Concierge --A2A--> Researcher --MCP--> "fetch" tool --> a web page
                  ^                  |
                  |                  +-- clock.now (the time it checked)
                  +---- facts -------+
you <-- briefing (Claude writes it) --- Concierge
```

- The **Researcher** (`researcher.ag`) reads a public page through an MCP tool, checks the clock, and
  asks Claude to write three sentences of facts. It is a small server you leave running.
- The **Concierge** (`concierge.ag`) asks the Researcher for facts over A2A, then asks Claude to turn
  them into a friendly three line briefing. You run it once per question.
- Both use **Claude Sonnet 5.5**. Which model, and where the key comes from, is written only in
  `metagente.toml`. Neither agent file names a provider or a model.

Each agent declares only what it needs, so the Concierge cannot read files and the Researcher cannot
call anything but the fetch tool and the clock.

This sample comes from the original project (MetaAgent, in Rust), changed only where this version works
differently: the Researcher is served at `/agents/Researcher`, every request to it carries a token, and the
fetch server is pinned to a version.

> **How this is tested:** `internal/cli/sample_test.go` runs this folder offline at every change: a fake
> model in place of Claude, a fake fetch server in place of `uvx mcp-server-fetch`, the Researcher really
> served and the Concierge really run. It was also run with the live Claude API and the real fetch server
> (check 11 of `validation/README.md`). Your output will differ from the one in "A sample question".

## Prerequisites

1. **Metagente.** Download the archive of your system from the releases of the project, or build it from
   the project folder with `make build` (the program is `bin/metagente`). The commands below say
   `metagente`; either put it on your PATH or type the full path.
2. **uv**, which starts the MCP fetch server for you (`uvx mcp-server-fetch==2026.8.18`). Install it from
   https://docs.astral.sh/uv/ and check with `uvx --version`. The first run downloads the fetch
   server, which takes about ten seconds.
3. **An Anthropic API key.** Get one from your Anthropic account. Model use is billed to you; one
   briefing is a few short questions to the model.
4. **Two terminals**, both opened in this folder (`samples/city-briefing/`). Metagente finds
   `metagente.toml` in the folder you start it from.

## Configuration

Everything you may want to change is in `metagente.toml`:

```toml
[llm]
provider = "anthropic"
model = "claude-sonnet-5-5"          # change only this line to use another model (both agents follow)
api_key_env = "ANTHROPIC_API_KEY"    # the NAME of the variable that holds your key, never the key itself

[runtime]
timeout_seconds = 90                 # a briefing needs a page fetch and two model answers
think_max_steps = 10                 # how many steps the model may take with its tools

[serve]
a2a_port = 8080                      # where the Researcher listens; the Concierge's address must match
bind = "127.0.0.1"                   # this computer only

[credentials]
Researcher = "RESEARCHER_TOKEN"      # the NAME of the variable that holds the token of the Researcher
```

The file never holds a secret. The key of the model and the token of the Researcher go in environment
variables, as the next section shows.

Two more things are written inside the agent files, because that is where Metagente declares them:

- `researcher.ag` says which MCP server to start: `tool fetch from mcp "uvx mcp-server-fetch==2026.8.18"`.
- `concierge.ag` says where the Researcher lives: `remote Researcher at "http://127.0.0.1:8080/agents/Researcher"`.

## Running the demo

**Once**, approve what each agent starts or reaches. Metagente runs nothing that starts a program or
reaches an address before you say yes:

```bash
metagente trust researcher.ag      # starts uvx
metagente trust concierge.ag       # reaches the Researcher, and sends it the token in RESEARCHER_TOKEN
```

**Once**, make the token that opens the Researcher, and keep it:

```bash
metagente token > .researcher-token
```

**Terminal 1**: start the Researcher and leave it running.

```bash
export ANTHROPIC_API_KEY=your-key-here
export METAGENTE_TOKEN=$(cat .researcher-token)
metagente serve researcher.ag
```

It prints where it listens. It only accepts connections from this computer; the demo never needs the
public option.

**Terminal 2**: ask the Concierge about a city.

```bash
export ANTHROPIC_API_KEY=your-key-here
export RESEARCHER_TOKEN=$(cat .researcher-token)
metagente run concierge.ag city=Lisbon
```

Wait a few seconds. Stop the Researcher with Ctrl-C when you are done, and remove `.researcher-token`.

## A sample question

You should see something like this (Claude's words will differ each time):

```text
Lisbon is the sunny capital of Portugal, built on seven hills beside the Tagus river.
It is known for trams, tiled buildings, custard tarts and a long history of explorers.
Wander the old Alfama streets, and try a tart before you leave.
```

Three short lines about the city, using only the facts the Researcher found. To see what the
Researcher alone produces, run it directly (no Concierge, no Terminal 1 needed):

```bash
metagente run researcher.ag city=Lisbon
```

It prints three sentences of facts, then the time it checked, for example
`(checked 2026-10-05T18:04:11.482913Z)`.

## How it works

`researcher.ag`:

```text
agent Researcher
  goal "Find facts about a city and summarize them"
  tool fetch from mcp "uvx mcp-server-fetch==2026.8.18"
  tool clock
  accepts research city  # find facts about a city
  on research
    if not city
      fail "I need a city"
    now = clock.now
    facts = think "Use the fetch tool to read https://en.wikipedia.org/wiki/{city} and write three short sentences of facts about {city}. If the page cannot be read, say that no facts were found."
    reply "{facts}\n(checked {now.text})"
```

`concierge.ag`:

```text
agent Concierge
  goal "Give a visitor a short briefing about a city"
  remote Researcher at "http://127.0.0.1:8080/agents/Researcher"
  accepts brief city  # a short briefing about a city
  on brief
    facts = Researcher.research city: city
    reply think "Write a friendly briefing of three lines for a visitor to {city}, using only these facts:\n{facts}"
```

Read them out loud: that is all they do. `Researcher.research city: city` is one line that sends a message
to another agent over A2A. `think` asks Claude, which may use only the tools that agent declared.

## Checking the Researcher from outside (A2A)

Any A2A client can talk to the Researcher, not only the Concierge, with the token. With Terminal 1
running, from any terminal of this computer:

```bash
export TOKEN=$(cat .researcher-token)

# Who is it and what can it do? (the agent card)
curl -H "Authorization: Bearer $TOKEN" http://127.0.0.1:8080/agents/Researcher/.well-known/agent-card.json

# Ask it for facts
curl -X POST http://127.0.0.1:8080/agents/Researcher -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":1,"method":"SendMessage","params":{"message":{"messageId":"m1","role":"ROLE_USER","parts":[{"text":"Lisbon"}]}}}'
```

The card lists one skill, `research`. The second command answers with a message of the agent whose text
holds the facts: a text alone goes to the one value of the one skill. Without the token both are refused
with `401`, and because the Researcher listens on this computer only, another computer cannot reach it.

## Things to try

- Change `model` in `metagente.toml` and run again. Both agents use the new model; no `.ag` file changes.
- Ask about another city: `metagente run concierge.ag city=Porto`.
- Ask for a place that has no page (`city=Xyzzyplugh`): the Researcher says no facts were found.

## Across two computers

To leave the Researcher running on another computer of your network (a Linux home server, say) and ask it
from the Concierge on your Mac, see [TWO-COMPUTERS.md](TWO-COMPUTERS.md): a certificate of your own, the
Researcher served on the network, the Mac told to trust it, and a service that keeps the Researcher up.

## Troubleshooting

| You see | What it means | What to do |
|---------|---------------|------------|
| `the language model could not answer: the variable ANTHROPIC_API_KEY is not set` | The terminal you ran in has no key | `export ANTHROPIC_API_KEY=...` in **that** terminal (both need it) |
| `the variable ANTHROPIC_API_KEY holds a line break, which cannot be part of a key` (or a space) | More than the key was copied into the variable, for example a command along with it | Put only the key in it; to check it without showing it: `echo ${#ANTHROPIC_API_KEY}` |
| `api.anthropic.com answered 401: invalid x-api-key` | The variable holds something that is not a key of your account | Copy the key again from the Anthropic Console, or make a new one |
| `have not approved for this project` | An agent starts a program or reaches an address you did not approve | Run `metagente trust` for that file, as in "Running the demo" |
| `I could not reach 127.0.0.1:8080 (remote agent Researcher)` | The Researcher is not running | Start Terminal 1: `metagente serve researcher.ag` |
| `answered 401 when I asked for the agent card of Researcher` | `RESEARCHER_TOKEN` is not the token the Researcher was started with | Use the same token in both terminals |
| `I could not start the tool server` | `uv` is not installed or not on your PATH | Install uv (see Prerequisites), then check `uvx --version` |
| `I need a city` | The city was empty | Pass one: `city=Lisbon` |
| `I could not listen on 127.0.0.1:8080` | Something else uses that port | Stop it, or change `a2a_port` in `metagente.toml` **and** the address in `concierge.ag` |
| `did not finish within 90 seconds` | The model or the page was slow | Try again, or raise `timeout_seconds` in `metagente.toml` |
| The answer says no facts were found | The page for that name does not exist | Try another spelling, or a bigger city |

If something else goes wrong, run `metagente check researcher.ag` and `metagente check concierge.ag`:
they point at the exact line of any mistake and say how to fix it.
