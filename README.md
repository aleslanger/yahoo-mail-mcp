# Yahoo Mail MCP (development baseline)

A local Go MCP server for bounded Yahoo IMAP reading and basic organization. It is **not production ready**. SMTP sending, drafts, attachment saving, deletion, batch operations and automation are not implemented. No live Yahoo mailbox or Codex/Claude Code invocation was available for verification.

## Build and configure

Requires Go 1.25 or later and network access to fetch pinned modules on a fresh machine.

```sh
go build -buildvcs=false -o yahoo-mcp ./cmd/yahoo-mcp
cp examples/config.json ./config.json
chmod 600 ./config.json
```

Edit `config.json` with an account alias and Yahoo address. Leave `allow_organize` false until you want folder creation, flag changes, copy and move. Generate a Yahoo app password in [Yahoo account security](https://help.yahoo.com/kb/technical-support/generate-password-access-yahoo-mail-sln15241.html). Supply it to the MCP process through a protected local environment or secret manager as `YAHOO_MCP_PASSWORD_PERSONAL`. Never put it in JSON, MCP configuration, a prompt or a shell command stored in history. Set `YAHOO_MCP_CONFIG` to the absolute path of `config.json`.

```sh
YAHOO_MCP_CONFIG=/absolute/path/config.json ./yahoo-mcp config-validate
YAHOO_MCP_CONFIG=/absolute/path/config.json ./yahoo-mcp serve
```

The `serve` command uses stdin/stdout only for MCP and writes diagnostics to stderr. It performs no network connection until a mail tool is invoked. The CLI also supports `version`.

## Connect clients

See [Codex setup](docs/CODEX_SETUP.md) and [Claude Code setup](docs/CLAUDE_CODE_SETUP.md). Configure the server command as the absolute path to `yahoo-mcp` with argument `serve`. The server needs `YAHOO_MCP_CONFIG` and the app-password environment variable in its process environment. There is no secure credential-setup CLI yet.

Example MCP flow: call `account_list`, `folder_list` with `account=personal`, then `email_list` with `account=personal`, `folder=INBOX`, `limit=20`. Read one result using `email_get` with its `uid` and `uid_validity`; raw MIME bytes are returned as base64 with an explicit `truncated` flag. For messages up to 256 KiB, `email_get_body` parses plain text, HTML and attachment metadata; `attachment_get` retrieves one attachment up to 64 KiB. `email_search` supports `from`, `subject`, `text`, and `unread` as an AND query. Use `next_before_uid` for another bounded page.

## Safety and limits

Folder creation, flags, copy and move are disabled per account by default. IMAP `MOVE` is used only if advertised; no fallback expunges messages. Message operations check UIDVALIDITY. A successful copy/move command is reported as submitted because final destination state cannot always be proven. Inspect both folders before retrying after an uncertain outcome. Search spans at most 5000 UID numbers per call; raw reads span at most 256 KiB. Sending and permanent deletion are absent.

See [tools](docs/TOOLS.md), [security](docs/SECURITY.md), [operations](docs/OPERATIONS.md) and [limitations](docs/LIMITATIONS.md). If authentication fails, verify the app password and account settings; if a folder fails, use `folder_list` to learn its exact name and delimiter. See [troubleshooting](docs/TROUBLESHOOTING.md).
