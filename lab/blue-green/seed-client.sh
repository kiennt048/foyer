#!/bin/sh
# Put one RADIUS client row into the client table and print its secret.
#
# This is what "adding an access point is a plain database write" looks like
# from the outside: no file is generated, no server is restarted, and the row is
# the whole of the change. The generator's change classification calls this
# sql-only, and this script is how that claim gets exercised rather than
# asserted.
#
# The secret is generated here, per client, and printed once. There is no
# default and there never will be: FreeRADIUS ships one, and a shipped shared
# secret is how appliance software gets mass-owned.
#
# Environment:
#   PGHOST PGPORT PGDATABASE PGUSER PGPASSWORD   as libpq expects
#   CLIENT_TABLE   default nas
#   CLIENT_IP      required — the source address the NAS sends from
#   CLIENT_NAME    default derived from CLIENT_IP
set -eu

CLIENT_TABLE=${CLIENT_TABLE:-nas}
: "${CLIENT_IP:?CLIENT_IP is required}"
CLIENT_NAME=${CLIENT_NAME:-$(printf 'ap-%s' "$CLIENT_IP" | tr '.:' '--')}

SECRET=${CLIENT_SECRET:-$(openssl rand -hex 24)}

psql -v ON_ERROR_STOP=1 -q -c "
	INSERT INTO $CLIENT_TABLE (nasname, shortname, type, secret)
	VALUES ('$CLIENT_IP', '$CLIENT_NAME', 'other', '$SECRET')
	ON CONFLICT (nasname) DO UPDATE
	  SET shortname = EXCLUDED.shortname,
	      type      = EXCLUDED.type,
	      secret    = EXCLUDED.secret
" >/dev/null

printf '%s\n' "$SECRET"
