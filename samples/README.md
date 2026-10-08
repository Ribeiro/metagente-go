# Samples

Small projects that show Metagente doing real work. Each one is a folder you can open, read, and run
on its own; the README inside says exactly how.

| Sample | What it shows |
|--------|---------------|
| [async-elt](async-elt/) | The Extractor and the Worker of an asynchronous ELT: the Extractor copies a table, page by page, into batch events on a message broker, with an outbox that lets it start again where it stopped. The Worker lands and transforms the batches in the destination, rejects bad rows with a code, and closes the job. |
| [city-briefing](city-briefing/) | Two agents working together. A **Concierge** asks a **Researcher** over A2A; the Researcher reads a web page through an MCP tool, and both use Claude to write. |

The samples come from the original project (MetaAgent, in Rust), changed only where this version works
differently. Each one is run offline by the tests of this project, so it keeps working as the program
changes.
