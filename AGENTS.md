# Agent Guidance

This repo uses RunQL for SQL workflows and schema exploration.

<!-- RUNQL:BEGIN -->
# RunQL Context

This workspace stores RunQL files locally under this project folder.

RunQL storage root:

./RunQL

Useful paths:

- Queries: ./RunQL/queries
- Query index: ./RunQL/system/queries/queryIndex.json (auto-updated when a query is saved)
- Schemas: ./RunQL/schemas
- Connection profiles: ./RunQL/system/connections.json
- Prompt templates: ./RunQL/system/prompts

## Required Workflow (SQL Queries)

1. Search for existing queries first — check the query index and `./RunQL/queries` (including subdirectories).
2. If nothing relevant exists, read the schema and docs under `./RunQL/schemas`. Use `./RunQL/schemas/<connection>/manifest.json` to find available schemas, then read only the relevant `./RunQL/schemas/<connection>/<schema>/schema.json` and `description.json`. Ignore `./RunQL/schemas/deleted/` and `*_deleted` folders unless the user asks for archived content.
3. Only then create a new SQL query file. Prefer to reuse or extend existing patterns. Put saved SQL under `./RunQL/queries/<connection>/`.

## Required Workflow (Documentation Requests)

1. **SQL query documentation:** follow `./RunQL/system/prompts/markdownDoc.txt`. Output goes in the same directory as the query with the same base name and a `.md` extension (e.g., `olympic_gold.sql` → `olympic_gold.md`).
2. **Schema description:** follow `./RunQL/system/prompts/describeSchema.txt`. Output goes to the matching bundle folder as `./RunQL/schemas/<connection>/<schema>/description.json`.
3. **Inline SQL comments:** follow `./RunQL/system/prompts/inlineComments.txt`.

Secrets are stored in VS Code SecretStorage and are not present in these files.
<!-- RUNQL:END -->
