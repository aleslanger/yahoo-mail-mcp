# Yahoo Mail MCP (development baseline)

A local Go MCP server for bounded Yahoo IMAP reading, basic organization, draft management and guarded text/HTML SMTP sending with small attachments, replies and bounded forwarding. It is **not production ready**. Permanent deletion and automation are not implemented. A guarded move to Trash is available when the account permits organization. The user reported successful folder creation, draft creation and draft sending from Codex. Live MCP stdio checks confirmed self-delivery, concurrent distinct tokens, replay after restart, pre-DATA recovery, flags, copy, move, Trash, batch flags, draft lifecycle/send, reply and forward. A controlled live test also hid successful SMTP and IMAP write replies and verified one actual delivery, a flag change and a move to Trash without replaying the writes. A 72-call read-only load run passed. Claude Code called the read-only connection tool successfully in an isolated session. Process termination, sustained writes and a dedicated disposable-account release test remain open. See the [production readiness checklist](docs/PRODUCTION_READINESS.md) for the remaining gates.

## Production status

The current release target is a supervised, single-user local stdio server. The remaining gates are process-termination and edge-case write recovery, longer load and disk-full behavior, security review on a release commit, and packaged-binary backup and rollback tests. Keep write permissions disabled except during supervised tests until those gates pass. See [production readiness](docs/PRODUCTION_READINESS.md), [testing](docs/TESTING.md) and [release procedure](docs/RELEASE.md).

## Build and configure

Requires Go 1.25 or later and network access to fetch pinned modules on a fresh machine.

```sh
make help
make build
make config-init
```

`make config-init` copies the example only when `config.json` does not exist and sets its permissions to 0600. Edit `config.json`: replace `your-address@example.com` with your full Yahoo address. Keep `alias` as `personal` for the commands below. Leave `allow_organize` and `allow_send` false for the first read-only test. The project defaults to Yahoo's [IMAP settings](https://help.yahoo.com/kb/new-mail-for-desktop/imap-smtp-settings-article-sln4075.html), so you do not need to change the host or port. Run `make config-check` to validate the JSON; this does **not** connect to Yahoo or verify the password.

Create an app password at [Yahoo Account Security](https://help.yahoo.com/kb/SLN15241.html) under **External connections → Create app password**. Use that password instead of your regular Yahoo password. If the option is unavailable, follow Yahoo's eligibility guidance on that page. Never put the password in JSON, MCP configuration, a prompt or a shell command stored in history.

### fish shell

Run this in the same fish shell from which you will launch your MCP client:

```fish
read --global --export --silent --prompt-str 'Yahoo app password: ' YAHOO_MCP_PASSWORD_PERSONAL
set -gx YAHOO_MCP_CONFIG (pwd)/config.json
test -n "$YAHOO_MCP_PASSWORD_PERSONAL"; and echo 'App password is set'
```

Type the app password only when prompted. Fish uses `--prompt-str` (`-P`) for a literal prompt; the Bash form `read -p` treats the text as a command in fish. `read --export` makes the password available to child processes without saving it in a file. If you change the alias, use `YAHOO_MCP_PASSWORD_<UPPERCASE_ALIAS>` instead.

For Codex, you can instead run `make codex` after registering the MCP server as shown in [Codex setup](docs/CODEX_SETUP.md). It prompts for the `personal` app password with hidden input and launches Codex with that password in its environment. The password is kept only for that Codex process tree; `make` cannot set it in the parent fish shell.

For Codex or an IDE that is launched separately, run `make credential-store` to enter the `personal` app password once. It saves the password as plaintext in `~/.config/yahoo-mail-mcp/personal-password` with file mode 0600 inside a 0700 directory. When the environment variable is absent, the server reads this protected file; an environment variable takes precedence. Restart the MCP client after updating the binary; a changed credential file is read on the next mail call. Remove the file to revoke local access. Use `make codex` instead if you do not want a persistent password file.

### Bash shell

```bash
read -r -s -p 'Yahoo app password: ' YAHOO_MCP_PASSWORD_PERSONAL
printf '\n'
export YAHOO_MCP_PASSWORD_PERSONAL
export YAHOO_MCP_CONFIG="$PWD/config.json"
```

Launch Codex or Claude Code from that shell so the MCP process inherits the password. If the client is already running, restart it from the configured shell or secret-manager launcher. Do not place the password in `~/.codex/config.toml` or `.mcp.json`.

For another Yahoo account, run `make account-add MAIL_ACCOUNT=work MAIL_ADDRESS=your-address@example.com`, then `make credential-store MAIL_ACCOUNT=work`. The account starts with read-only permissions. `make account-list` lists aliases and permissions; `make account-remove MAIL_ACCOUNT=work` removes only the JSON entry and leaves any password file for manual cleanup. Restart the MCP client after account configuration changes.

Run `make account-test` after setting the environment variable or storing the private credential file. It tests TLS, IMAP login and capability negotiation without reading messages or changing the mailbox. For another alias, run `make account-test MAIL_ACCOUNT=work`. A successful run prints `IMAP login OK for personal` and the number of capabilities; it never prints the password. If it says the app password is not configured, provide one of the two credential sources. `make smtp-test` checks SMTP TLS and login for the same account without sending a message. `make config-check` only validates local JSON.

In your MCP client, call `account_test_connection` with `account: "personal"`, then `folder_list` or `folder_tree` with the same account, then `email_list` with `account: "personal"`, `folder: "INBOX"`, `limit: 20`. For folder message and unread counts, call `folder_tree` with `account: "personal"` and `include_counts: true`. It checks up to 20 folders per call and returns `next_count_offset` when more remain. The MCP client must inherit the same app-password variable from its launcher. To save an attachment without returning its bytes to the model, set top-level `download_dir` in `config.json` to an absolute path for a private 0700 directory, then call `attachment_save` with the message reference and attachment index. The tool creates a random 0600 file there and returns its path; its limits are 16 MiB per message and 10 MiB per attachment.

To enable sending, set `allow_send: true` on that account and set top-level `journal_dir` to an absolute path for a private local directory. The server creates it with mode 0700. `email_prepare` validates recipients and content and returns a digest. Pass the same content, a unique `idempotency_key`, and that digest as `expected_digest` to `email_send`. The server uses authenticated TLS on `smtp.mail.yahoo.com:465`; port 587 STARTTLS is not implemented. Once message submission begins, a repeated key returns its prior state without another SMTP submission. If connection or authentication fails before the message body starts, the same key can be retried after fixing the problem.

The `serve` command uses stdin/stdout only for MCP and writes diagnostics to stderr. It performs no network connection until a mail tool is invoked. The CLI also supports `version`.

## Makefile commands

`make help` lists every target. `make build` rebuilds the current source. `make config-init` preserves an existing account configuration, `make config-check` validates it, and `make storage-status` reports journal and saved-attachment file counts and logical bytes without logging in; its alerts and exit code 3 indicate an initial review threshold. `make account-test` tests IMAP login, `make smtp-test` tests SMTP login without sending, `make account-add MAIL_ACCOUNT=work MAIL_ADDRESS=your-address@example.com` adds a read-only Yahoo account, `make account-list` lists configured aliases, `make account-remove MAIL_ACCOUNT=work` removes an alias, `make credential-store MAIL_ACCOUNT=work` saves that account’s app password in a private file, and `make codex` prompts for the password and starts Codex. `make check` runs tests, race tests, vet and build. To update an existing Git checkout, run `make update`: it requires a clean working tree and a configured upstream branch, performs a fast-forward-only pull, then runs `make check`. It does not update Go dependencies independently. Keep your ignored `config.json` and app password outside Git.

## Connect clients

See [Codex setup](docs/CODEX_SETUP.md) and [Claude Code setup](docs/CLAUDE_CODE_SETUP.md). Configure the server command as the absolute path to `yahoo-mcp` with argument `serve`. The server needs `YAHOO_MCP_CONFIG` and either the app-password environment variable or the private credential file described above.

Example MCP flow: call `account_list`, `account_test_connection` with `account=personal`, `folder_list` with `account=personal`, then `email_list` with `account=personal`, `folder=INBOX`, `limit=20`. Read one result using `email_get` with its `uid` and `uid_validity`; raw MIME bytes are returned as base64 with an explicit `truncated` flag and `next_offset` for another 256 KiB range. For messages up to 16 MiB, `email_get_body` parses plain text, HTML and attachment metadata (each text part is capped at 64 KiB and reports truncation); `attachment_get` retrieves one attachment up to 64 KiB. `attachment_save` can write a larger attachment to a private local file when `download_dir` is set. `email_search` supports `from`, `to`, `subject`, `text`, `since`, `before`, `unread`, and `flagged` as an AND query; dates are YYYY-MM-DD with an exclusive `before`. `email_next_unread` returns the newest unread message in the current UID window. Use `next_before_uid` for another bounded page. `draft_create`, `draft_list`, `draft_get`, `draft_update` and `draft_delete` operate on a discovered Drafts folder when organize permission is enabled and UIDPLUS/IMAP4rev2 is available; updates return a new UID. `draft_prepare_send` previews the exact stored MIME and `draft_send` sends it with the same digest, then deletes the draft only after confirmed SMTP acceptance. Supply Bcc again because it is absent from stored MIME. For a reply, call `email_reply_prepare` with the source message reference, your new text and a unique key; pass the same inputs plus its digest to `email_reply_send`. `reply_all` adds other recipients and `quote_original` includes a complete small plain-text source. `email_reply_context` also supplies threading fields for `email_prepare`/`email_send`. For a plain-text forward, call `email_forward_prepare` with explicit recipients and optional `original_attachment_indexes`, then call `email_forward_send` with the returned digest. To move a message to Trash, enable `allow_organize` and call `email_trash`; the server requires a unique Trash folder or an explicit `trash_folder` account setting. For multiple exact UIDs, call `email_batch_prepare` and inspect its preview, then call `email_batch_execute` with its token and `confirm=true`.

## Safety and limits

Folder creation, flags, copy, move, draft changes and sending are disabled per account by default. IMAP `MOVE` is used only if advertised; no fallback expunges messages. Message operations check UIDVALIDITY. Copy and move check source state and verify the destination UID when the server supplies a mapping. Inspect both folders before retrying after an uncertain outcome. Search spans at most 5000 UID numbers per call; raw reads use up to 256 KiB per request. SMTP sending is limited to text and HTML with at most 50 recipients, 128 KiB per body part and 10 base64 attachments (1 MiB each, 5 MiB total decoded). A lost SMTP acknowledgment is recorded as uncertain and never retried automatically. Permanent deletion is absent; `email_trash` performs a checked MOVE and never expunges.

See [tools](docs/TOOLS.md), [security](docs/SECURITY.md), [operations](docs/OPERATIONS.md) and [limitations](docs/LIMITATIONS.md). If authentication fails, verify the app password and account settings; if a folder fails, use `folder_list` to learn its exact name and delimiter. See [troubleshooting](docs/TROUBLESHOOTING.md).
