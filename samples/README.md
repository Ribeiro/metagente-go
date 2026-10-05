# Samples

Small projects that show Metagente doing real work. Each one is a folder you can open, read, and run
on its own; the README inside says exactly how.

| Sample | What it shows |
|--------|---------------|
| [city-briefing](city-briefing/) | Two agents working together. A **Concierge** asks a **Researcher** over A2A; the Researcher reads a web page through an MCP tool, and both use Claude to write. |

The samples come from the original project (MetaAgent, in Rust), changed only where this version works
differently. Each one is run offline by the tests of this project, so it keeps working as the program
changes.
