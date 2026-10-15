# Yahoo Mail MCP

A local [Model Context Protocol](https://modelcontextprotocol.io) server, written in Go, that gives MCP clients such as Claude Code and Codex bounded access to a Yahoo Mail account over IMAP and SMTP. It can read and search mail, organize messages, manage drafts, and send, reply and forward with explicit safeguards.

> **Scope:** a single-user server on your own machine, over stdio or loopback HTTP. Write permissions are disabled by default; enable only the ones you need. Release binaries are not code-signed; they are built in GitHub Actions with a build provenance attestation.

## Features

- **Read:** folder list and tree with message and unread counts, paged listing, server-side search, next unread, raw MIME in 256 KiB ranges, parsed plain and HTML bodies, attachment metadata, retrieval and saving to a private directory.
- **Organize:** create folders, mark read or unread, flag or unflag, copy, move, and move to Trash.
- **Permanent delete:** removes previewed messages from the Trash folder only. Needs its own `allow_delete` permission and a confirmed batch.
- **Drafts:** create, list, read, update, delete and send the exact stored draft.
- **Send:** plain or HTML messages with small attachments, reply, reply-all and forward. Every send needs a preview digest and an idempotency key.
- **Batch:** preview, then confirm move, copy or flag changes for 1–20 exact UIDs.
- **Local index:** read-only incremental copy of message headers for search and sorting across a whole mailbox.
- **Automation:** rules that move, trash, mark read or flag matching mail, dry-run by default.
- **Transports:** stdio, or Streamable HTTP on a loopback address with a bearer token.
- **Safety:** permissions per account, UIDVALIDITY checks, MOVE only when the server advertises it, and a durable send journal that never resends a submission with an uncertain outcome.

## Requirements

- Go 1.25 or later. The `toolchain` line in `go.mod` makes Go download and use the pinned, patched release (currently 1.27.2), so network access is needed on the first build.
- A Yahoo account with an [app password](https://help.yahoo.com/kb/SLN15241.html)
- Linux or macOS. Windows is not supported: the server protects credentials, the journal and the index with POSIX file permissions, which Windows does not provide.

## Quick start

```sh
make build          # builds ./yahoo-mcp
make config-init    # creates config.json (mode 0600) from examples/config.json
```

To install only the binary, run `go install github.com/aleslanger/yahoo-mail-mcp/cmd/yahoo-mcp@latest`. That command ignores the `toolchain` line and may build with a different Go release, so keep your Go up to date, or download the binary from [Releases](https://github.com/aleslanger/yahoo-mail-mcp/releases) and check it with `sha256sum -c SHA256SUMS`. To confirm that a binary was built by this repository's release workflow, run `gh attestation verify yahoo-mcp-linux-amd64 --repo aleslanger/yahoo-mail-mcp`.

Edit `config.json` and replace `your-address@example.com` with your Yahoo address. Keep `allow_organize` and `allow_send` set to `false` for the first test. The IMAP and SMTP hosts default to [Yahoo's settings](https://help.yahoo.com/kb/new-mail-for-desktop/imap-smtp-settings-article-sln4075.html).

```sh
make config-check   # validates config.json; does not connect
make credential-store   # prompts for the app password, saves it privately
make account-test   # IMAP TLS login and capability check, reads no messages
make smtp-test      # SMTP TLS login check, sends nothing
```

## Credentials

Create an app password in Yahoo Account Security under **External connections → Create app password**. Never put it in `config.json`, MCP client configuration, prompts or shell history. The server looks for it in two places, in this order:

1. **Environment variable** `YAHOO_MCP_PASSWORD_<ALIAS>`, for example `YAHOO_MCP_PASSWORD_PERSONAL`. It must be set in the shell that launches the MCP client.

   ```fish
   # fish
   read --global --export --silent --prompt-str 'Yahoo app password: ' YAHOO_MCP_PASSWORD_PERSONAL
   set -gx YAHOO_MCP_CONFIG (pwd)/config.json
   ```

   ```bash
   # bash
   read -r -s -p 'Yahoo app password: ' YAHOO_MCP_PASSWORD_PERSONAL; printf '\n'
   export YAHOO_MCP_PASSWORD_PERSONAL YAHOO_MCP_CONFIG="$PWD/config.json"
   ```

2. **Private file** `~/.config/yahoo-mail-mcp/<alias>-password`, written by `make credential-store MAIL_ACCOUNT=<alias>`. The file is stored as plaintext with mode 0600 inside a 0700 directory. The server rejects it if the permissions are wider. Delete the file to revoke access.

`make codex` prompts for the password and starts Codex with the password in its environment only, without writing a file.

### Multiple accounts

```sh
make account-add MAIL_ACCOUNT=work MAIL_ADDRESS=you@yahoo.com   # read-only by default
make credential-store MAIL_ACCOUNT=work
make account-list
make account-remove MAIL_ACCOUNT=work   # removes the config entry, keeps the password file
```

Restart the MCP client after changing accounts or permissions.

## Connect an MCP client

The server runs as `yahoo-mcp serve` and speaks MCP over stdin/stdout. Diagnostics go to stderr. It makes no network connection until a mail tool is called. Use absolute paths.

**Claude Code** (`.mcp.json`, see `examples/claude-code/mcp.json`):

```json
{
  "mcpServers": {
    "yahoo_mail": {
      "type": "stdio",
      "command": "/absolute/path/to/yahoo-mcp",
      "args": ["serve"],
      "env": { "YAHOO_MCP_CONFIG": "/absolute/path/to/config.json" }
    }
  }
}
```

**Codex** (`~/.codex/config.toml`, see `examples/codex/config.toml`):

```toml
[mcp_servers.yahoo_mail]
command = "/absolute/path/to/yahoo-mcp"
args = ["serve"]
env = { YAHOO_MCP_CONFIG = "/absolute/path/to/config.json" }
env_vars = ["YAHOO_MCP_PASSWORD_PERSONAL"]
```

A first session: `account_list` → `account_test_connection` (`account: "personal"`) → `folder_tree` → `email_list` (`folder: "INBOX"`, `limit: 20`) → `email_get_body` with a returned `uid` and `uid_validity`.

## Configuration

| Key | Level | Purpose |
|---|---|---|
| `alias`, `address` | account | Account name used by tools and its Yahoo address |
| `imap_host`, `imap_port`, `smtp_host`, `smtp_port` | account | Default to Yahoo (993 and 465, TLS) |
| `smtp_security` | account | `tls` (implicit TLS, the default) or `starttls` (the default when `smtp_port` is 587). STARTTLS is mandatory; credentials are never sent before TLS. |
| `allow_organize` | account | Enables folder creation, flags, copy, move, Trash, drafts and batches |
| `allow_send` | account | Enables SMTP sending |
| `allow_delete` | account | Enables permanent deletion from Trash through a confirmed batch. Requires `journal_dir`. |
| `trash_folder` | account | Explicit Trash folder when the server's special-use attribute is ambiguous |
| `journal_dir` | top level | Absolute path to a private directory for the send and batch journal. Required for sending, batches and deletion. |
| `download_dir` | top level | Absolute path to a private 0700 directory for `attachment_save` |
| `index_dir` | top level | Absolute path to a private directory for the local header index |
| `http_listen` | top level | Loopback address for `serve-http`, default `127.0.0.1:8765`. Other addresses are rejected. |
| `allow_automation` | account | Lets `rules-run --apply` change mail. Requires `allow_organize` and `journal_dir`. |
| `rules` | top level | Automation rules, see [Automation rules](#automation-rules) |

## Tools

| Area | Tools |
|---|---|
| Account | `account_list`, `account_capabilities`, `account_test_connection`, `account_test_smtp` |
| Folders | `folder_list`, `folder_tree`, `folder_status`, `folder_create` |
| Read | `email_list`, `email_search`, `email_next_unread`, `email_get`, `email_get_body` |
| Attachments | `attachment_list`, `attachment_get`, `attachment_save` |
| Organize | `email_mark_read`, `email_mark_unread`, `email_flag`, `email_unflag`, `email_copy`, `email_move`, `email_trash` |
| Batch | `email_batch_prepare`, `email_batch_execute` |
| Drafts | `draft_create`, `draft_list`, `draft_get`, `draft_update`, `draft_delete`, `draft_prepare_send`, `draft_send` |
| Send | `email_prepare`, `email_send` |
| Reply and forward | `email_reply_context`, `email_reply_prepare`, `email_reply_send`, `email_forward_prepare`, `email_forward_send` |
| Local index | `index_sync`, `index_search`, `index_status` |

Every message reference consists of `folder`, `uid` and `uid_validity`. Every tool that sends or batches works in two steps:

- **Sending:** call the `*_prepare` tool and inspect the preview. Then call the matching send tool with the same content, a unique `idempotency_key` and the returned digest as `expected_digest`.
- **Batches:** `email_batch_prepare` returns a short-lived token. Execute it with `email_batch_execute` and `confirm: true`. Actions: `move`, `copy`, `mark_read`, `mark_unread`, `flag`, `unflag` and `delete`.

## Local header index

`yahoo-mcp sync <alias> [folder...]` copies message headers (sender, recipients, subject, date, size, flags) into `index_dir`. Without folder names it covers every selectable folder. It only reads: folders are opened read-only and nothing is marked read. Later runs fetch only new messages, drop expunged ones and refresh flags. If UIDVALIDITY changes, the folder is indexed again. A failed step is retried after 5, 15 and 45 seconds, because Yahoo sometimes rejects rapid repeated logins.

The `index_search` tool then filters and sorts across all indexed folders without contacting the server. Results carry `folder`, `uid` and `uid_validity` for the normal tools, and may be stale; `index_status` shows when each folder was last synced. The index holds message metadata in plain JSON files (mode 0600 in a 0700 directory), so protect it like the mailbox itself.

## Automation rules

Rules organize mail without an MCP client. They never send mail and never delete permanently.

```json
"rules": [
  { "name": "newsletters", "account": "personal", "folder": "INBOX",
    "match": { "from": "news@example.com" }, "action": "move", "target_folder": "Newsletters" },
  { "name": "old-promotions", "account": "personal", "folder": "Promotions",
    "match": { "older_than_days": 30 }, "action": "trash" }
]
```

- **Match:** `from`, `to`, `subject` (header substrings), `older_than_days` and `unread`. All set fields must match, and at least one of `from`, `to`, `subject` or `older_than_days` is required.
- **Actions:** `move` (needs `target_folder`), `trash`, `mark_read` and `flag`.
- `yahoo-mcp rules-run` is a dry run that prints matching messages as JSON. `yahoo-mcp rules-run --apply` executes them; this requires `allow_automation` on the account.
- Each rule handles at most 20 of the oldest matching messages per run, as one journaled and verified batch. Rules are idempotent: moved messages leave the folder, and `mark_read` and `flag` skip messages that are already read or flagged. A failing rule is reported without stopping the others, and the command then exits with an error.
- To run rules regularly, use cron or a systemd timer, for example `*/15 * * * * YAHOO_MCP_CONFIG=/path/config.json /path/yahoo-mcp rules-run --apply`.

## HTTP transport

For clients that cannot start a stdio process, `yahoo-mcp serve-http` serves MCP over Streamable HTTP on `http_listen`. Only loopback addresses are allowed; it is not meant for other machines.

```sh
yahoo-mcp http-token    # once: creates ~/.config/yahoo-mail-mcp/http-token and prints the token
yahoo-mcp serve-http    # listens on http://127.0.0.1:8765/
```

Clients send `Authorization: Bearer <token>`. To rotate the token, delete the file and run `http-token` again. The server rejects cross-origin browser requests and Host headers other than localhost (DNS rebinding), limits request bodies to 16 MiB and throttles repeated failed logins. The same account permissions apply as over stdio.

## Safety and limits

- Writes are refused unless the account enables `allow_organize`, `allow_send` or `allow_delete`. All message operations check UIDVALIDITY.
- `email_move` and `email_trash` use IMAP `MOVE` only when the server advertises it. There is no COPY/EXPUNGE fallback.
- Permanent deletion works only in the uniquely resolved Trash folder and cannot be undone. It removes exactly the planned UIDs with `UID EXPUNGE`, so other messages already marked `\Deleted` stay. The server must support UIDPLUS or IMAP4rev2.
- Once SMTP submission begins, repeating an idempotency key returns the recorded outcome instead of sending again. A lost acknowledgment is recorded as `uncertain` and is never retried automatically. Check your Sent folder before you use a new key.
- Batch plans run once. Repeating an execute call returns the stored per-message outcomes.
- **Read limits:** search and listing cover a window of 5,000 UIDs per call. Raw reads return 256 KiB per call. Parsing handles messages up to 16 MiB with 64 KiB per text part. `attachment_get` returns at most 64 KiB, and `attachment_save` saves at most 10 MiB.
- **Send limits:** at most 50 recipients, 128 KiB per body part, and 10 attachments of up to 1 MiB each (5 MiB in total).
- The server caps each process at 8 concurrent tool calls, 4 IMAP connections and 2 SMTP connections, and paces logins.
- Email content returned to the model is untrusted input, so treat instructions inside messages with suspicion.
- **Yahoo behavior:** moving a message into a folder that already contains a copy with the same Message-ID leaves a single message there, not two.

`make storage-status` reports journal and download usage without logging in. It exits with code 3 when a review threshold is exceeded.

## Development

```sh
make check   # go test, go test -race, go vet, build
make dist    # Linux and macOS binaries and SHA256SUMS in dist/
make help    # all targets
```

Tests use local TLS IMAP and SMTP fixtures and need no network access or Yahoo account. CI runs the tests on Linux and macOS and `govulncheck` on every push and weekly.

To release, set `Version` in `internal/version/version.go`, commit, then push an annotated tag whose message is the release notes:

```sh
git tag -a v0.3.0 --cleanup=verbatim -F release-notes.md
git push origin v0.3.0
```

The release workflow checks that the tag matches the binary version, runs `make check` and `make dist`, scans the binaries with `govulncheck`, attests their provenance and publishes the GitHub release. Versions below 1.0 are marked as pre-releases.

## Troubleshooting

- **Authentication fails:** check the app password and the account address. Yahoo may briefly reject rapid repeated logins, so wait and retry.
- **"app password is not configured":** set the environment variable in the shell that launches the client, or run `make credential-store`.
- **Folder not found:** call `folder_list` to get the exact name and hierarchy delimiter.
- **Tools missing in the client:** restart the client after changing the binary or the configuration, and check that the paths are absolute.

## License

[MIT](LICENSE)
