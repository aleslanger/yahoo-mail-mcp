#!/usr/bin/env bash
set -euo pipefail

account_alias="${1:-personal}"
if [[ ! "$account_alias" =~ ^[a-z][a-z0-9_]{0,31}$ ]]; then
    printf 'Invalid account alias.\n' >&2
    exit 1
fi
credential_dir="$HOME/.config/yahoo-mail-mcp"
credential_file="$credential_dir/$account_alias-password"
umask 077
if [[ -L "$credential_dir" ]]; then
    printf 'Credential directory must not be a symbolic link.\n' >&2
    exit 1
fi
mkdir -p -- "$credential_dir"
chmod 700 -- "$credential_dir"

if [[ ! -r /dev/tty ]]; then
    printf 'An interactive terminal is required.\n' >&2
    exit 1
fi

password=
if ! IFS= read -r -s -p 'Yahoo app password: ' password < /dev/tty; then
    printf '\nUnable to read the password.\n' > /dev/tty
    exit 1
fi
printf '\n' > /dev/tty
if [[ -z "$password" ]]; then
    printf 'The password cannot be empty.\n' >&2
    exit 1
fi

temporary_file="$(mktemp "$credential_dir/.$account_alias-password.XXXXXX")"
trap 'rm -f -- "$temporary_file"' EXIT
printf '%s\n' "$password" > "$temporary_file"
unset password
chmod 600 -- "$temporary_file"
mv -f -- "$temporary_file" "$credential_file"
trap - EXIT
printf 'Yahoo app password saved for account %s.\n' "$account_alias"
