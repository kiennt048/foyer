#!/usr/bin/env bash
#
# What a backup is, and how to find one. Sourced, never executed.
#
# `${FOYER_ROOT}` and `${STATE}` must already be set.
#
# ── why this file exists ─────────────────────────────────────────────────────
#
# `deploy/images.sh` (image pins), `deploy/secretdirs.sh` (the directories the
# container writes) and `deploy/consolenames.sh` (the console certificate's
# names) each exist because **a rule that lives in one script is a rule the
# other scripts do not have**, and the cost is a real outage — for instance a
# console certificate the control plane cannot write, because `up.sh` never runs
# the thing `first-boot.sh` does.
#
# The same holds here. Three scripts need to agree about what a backup is and how
# one is named and fingerprinted:
#
#   backup.sh    takes one, prunes, and prints the inventory
#   restore.sh   picks one, checksums it, and matches a KEK against its manifest
#   verify.sh    answers "is there a recent one, and is it restorable in
#                principle" — which it cannot answer if it guesses at a filename
#
# **A fingerprint computed two ways is a fingerprint that eventually disagrees
# with itself**, and the disagreement would be reported as "this backup does not
# match your KEK" on an installation where nothing was wrong. So there is one
# definition of each, here, and the other three call it.
#
# ── the three facts, and what each is for ────────────────────────────────────
#
# A dump is `foyer-<UTC timestamp>.dump`, beside a `foyer-<same>.manifest`. The
# name carries the timestamp so an operator can read one out over the phone; the
# manifest carries the three things a restore cannot work out for itself:
#
#   sha256      is this the file that was written, or a copy that has gone bad on
#               a disk nobody checks? Checked **before** anything is stopped.
#   schema      which goose version is inside, so restoring a dump from a newer
#               tree into an older one is a known downgrade rather than a
#               surprise found by `pg_restore`.
#   kek-sha256  **is there a KEK anywhere that opens this?** It is a fingerprint
#               of the key, never the key: a manifest carrying the KEK would be a
#               second copy of it in a directory with weaker modes than
#               `secrets/`, and a copy of the backups is exactly what leaves this
#               host.
#
BACKUPS="${STATE}/backups"

# mtime <file> — seconds, on a Debian host and on a macOS workstation. The
# difference is `stat -c` versus `stat -f`, which is the whole portability story
# of this file and is written once.
foyer_mtime() {
  stat -c %Y "$1" 2>/dev/null || stat -f %m "$1" 2>/dev/null || echo 0
}

# foyer_sha256 <file> — the hash, in the `<hash>  <path>` form `sha256sum -c`
# reads, on both.
foyer_sha256() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | cut -d' ' -f1
  else shasum -a 256 "$1" | cut -d' ' -f1; fi
}

# foyer_kek_fingerprint <file> — SHA-256 of a KEK file's **contents**, with the
# trailing newline trimmed.
#
# The trim is load-bearing and not cosmetic. `first-boot.sh`'s generator is
# `printf '%s:%s\n'`, so the file on disk ends in a newline that is **not part of
# the key**: `internal/config`'s `EnvOrFile` trims exactly this one newline
# before handing the value to the keyring. Hashing the file raw would make the
# fingerprint depend on whether a tool rewrote it with a newline, and the answer
# to "does my KEK open this backup" must not depend on that.
foyer_kek_fingerprint() {
  [[ -s "$1" ]] || return 1
  local k
  # **`cat` has to succeed, and `$(...)` throws its exit status away.**
  #
  # The KEK is 0400 owned by the container's uid and the backup runs as the
  # deploy user, so a plain `cat` prints `Permission denied` to stderr and the
  # command substitution returns the EMPTY STRING — whose SHA-256 is
  # `e3b0c442…b855`, a perfectly ordinary-looking digest. The manifest would
  # record it, `verify.sh` would compare against it, and the operator would be
  # told to keep a copy of "that file": a real backup, a zero exit, and a
  # fingerprint of nothing.
  #
  # `sudo` is the same door `verify.sh` already uses, granted by `bootstrap.sh`.
  # The `|| return 1` is the part that matters: **a fingerprint that could not be
  # computed must never be recorded as a fingerprint.**
  k="$(sudo -n cat "$1" 2>/dev/null || cat "$1")" || return 1
  [[ -n "${k}" ]] || return 1
  printf '%s' "${k}" | foyer_sha256 /dev/stdin
}

# foyer_backups_newest_first — the dump paths, newest first, one per line.
#
# Not `ls -t`, and not a glob either: a glob comes back in collation order, which
# is alphabetical, and `foyer-20260928T031700Z` sorts fine but the day a dump is
# named after a revision rather than a clock it stops being true. Sorting on the
# mtime is the property "newest first" actually means, and `backup.sh`'s pruning
# is only correct if it is.
foyer_backups_newest_first() {
  local f
  for f in "${BACKUPS}"/*.dump; do
    [[ -e "${f}" ]] || continue
    printf '%s\t%s\n' "$(foyer_mtime "${f}")" "${f}"
  done | sort -rn -k1,1 | cut -f2-
}

# foyer_backup_manifest_field <dump> <key> — one field out of a dump's manifest,
# or nothing. An absent manifest yields nothing rather than a made-up default,
# because a restore that cannot read the KEK fingerprint has to say so.
foyer_backup_manifest_field() {
  local dump="$1" key="$2"
  [[ -s "${dump%.dump}.manifest" ]] || return 1
  awk -v k="${key}:" '$1 == k {print $2; found=1} END {exit !found}' "${dump%.dump}.manifest"
}

# foyer_backup_list — the inventory, as `name<TAB>age<TAB>kek fingerprint`.
#
# **The fingerprint is truncated nowhere here.** `backup.sh` shortens it for its
# own summary and `verify.sh` prints the whole thing; this function returns the
# value in full so that a caller comparing it against a KEK is comparing the
# whole thing. A truncated fingerprint compared with `==` is a check that passes
# on a prefix, and a prefix of a SHA-256 is not a SHA-256.
foyer_backup_list() {
  local f name age fp
  while IFS= read -r f; do
    [[ -n "${f}" ]] || continue
    name="$(basename "${f}")"
    age=$(( ( $(date +%s) - $(foyer_mtime "${f}") ) / 3600 ))
    fp="$(foyer_backup_manifest_field "${f}" kek-sha256 || echo 'NO MANIFEST')"
    printf '%s\t%sh\t%s\n' "${name}" "${age}" "${fp}"
  done < <(foyer_backups_newest_first)
}
