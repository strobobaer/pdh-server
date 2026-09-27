# RunQL Project

This project uses RunQL for SQL workflows and schema exploration.

Storage mode: **workspace-local**

Storage root: `./RunQL`

## Setup

Recommended `.gitignore` entry:

```gitignore
RunQL/system/
```

*Note: `RunQL/queries/` and `RunQL/schemas/` SHOULD be committed as they contain your source artifacts.*

## Folder Structure

- **queries/**: Saved SQL queries.
- **queries/<connection>/**: Saved SQL queries grouped by connection.
- **schemas/<connection>/manifest.json**: Lists schema bundles for a connection.
- **schemas/<connection>/<schema>/**: Per-schema schema bundle including descriptions and ERD files.
- **system/**: Generated indexes, migration backups, and prompt templates.
