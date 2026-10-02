-- An erasure reaches the audit log: the event stays, the identifier does not.
--
-- ── the problem ───────────────────────────────────────────────────────────────
--
-- After an erasure by MAC or by account, `audit_log` still named the guest for up
-- to 730 days: `session-disconnected` carries the session's Calling-Station-Id as
-- `subject_name` (internal/api/handlers_disconnect.go), the portal-grant variant the
-- grant's MAC (handlers_portalgrants.go), and `account-credentials-changed` the
-- account's username (handlers_people.go). The coverage test matched column *names*
-- against a regular expression, and `subject_name` is not a name that says "MAC".
--
-- ── what the field does, and the decision ────────────────────────────────────
--
-- Deleting the entries would be the wrong repair: an audit log is the operator's
-- accountability record, and "somebody disconnected a guest on 3 May" is a fact the
-- venue may need after the guest has asked to be forgotten. GDPR Art. 17(3)(e) and
-- Law 91/2025/QH15 both leave room to keep what a legal claim needs; what they do
-- not leave room for is keeping *who the guest was*. The field's practice:
--
--   * **authentik** has a system setting, `GDPR compliance`, on by default, that
--     *deletes* the events a user caused when the user is deleted, and documents
--     turning it off where audit requirements outweigh it. Foyer diverges, and the
--     difference is whose events they are: authentik's are the deleted user's own
--     actions; Foyer's entries are an *operator's* actions on a guest, and deleting
--     them would let an erasure remove the record of what staff did;
--   * **PacketFence** keeps `admin_api_audit_log` on its own cleanup window,
--     independent of node deletion;
--   * **EDPB Guidelines 01/2025 on pseudonymisation** and ENISA's pseudonymisation
--     techniques paper name the keyed hash (HMAC) under a key held apart from the
--     data as the standard technique: the same subject maps to the same pseudonym,
--     so an administrator can still see that two entries were about one guest, and
--     nobody without the key can go back.
--
-- So an erasure replaces every spelling of the identifier in `subject_name`,
-- `subject_id` and `params` with `pseudonym:` + 16 hex digits of HMAC-SHA256 under a
-- subkey HKDF-derived from FOYER_KEK (store/auditpseudonym.go). The key is outside
-- the database by the same rule that keeps FOYER_KEK out of it.
--
-- ── the privilege, which is the part that must not grow ──────────────────────
--
-- `foyer_app` keeps SELECT and INSERT on `audit_log` and gains **no UPDATE**
-- (readonly_test.go still asserts the refusal). It gains EXECUTE on one function,
-- `foyer_audit_pseudonymise`, which can only ever *replace* a MAC-shaped or exactly
-- named identifier with a string of the pseudonym's fixed shape. It cannot write
-- arbitrary text, cannot touch `at`, `kind`, `actor_*` or `site_id`, and a literal
-- identifier is only replaced on entries about a guest (a session, a portal grant,
-- an account, a consent, an erasure), so it cannot be pointed at a venue's or an
-- operator's name.
--
-- The function is SECURITY DEFINER and owned by `foyer_audit_redactor`, a NOLOGIN
-- role holding SELECT and UPDATE (subject_id, subject_name, params) on audit_log and
-- nothing else -- the 00019 pattern for the credential view: never owned by a
-- superuser (`TestNoSuperuserOwnedFunctionRunsForALoginRole`), and its search_path is
-- pinned so a login role cannot shadow a function it calls.

-- +goose Up

-- +goose StatementBegin
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'foyer_audit_redactor') THEN
        CREATE ROLE foyer_audit_redactor NOLOGIN;
    END IF;
    IF NOT pg_has_role(current_user, 'foyer_audit_redactor', 'MEMBER') THEN
        EXECUTE format('GRANT foyer_audit_redactor TO %I', current_user);
    END IF;
END
$$;
-- +goose StatementEnd

GRANT USAGE ON SCHEMA public TO foyer_audit_redactor;
GRANT SELECT ON audit_log TO foyer_audit_redactor;
GRANT UPDATE (subject_id, subject_name, params) ON audit_log TO foyer_audit_redactor;

-- audit_log's policies are FOR SELECT and FOR INSERT (00003) and FOR DELETE TO
-- foyer_retention (00020). Under FORCE, an UPDATE with no policy matches nothing and
-- reports success -- the silent shape again -- so the redactor gets one, and only it.
CREATE POLICY audit_log_pseudonymise ON audit_log FOR UPDATE TO foyer_audit_redactor
    USING (tenant_id = foyer_current_tenant()) WITH CHECK (tenant_id = foyer_current_tenant());

-- +goose StatementBegin
CREATE FUNCTION foyer_audit_pseudonymise(p_mac_hex text, p_literals text[], p_pseudonym text)
RETURNS bigint
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, public
AS $$
DECLARE
    guest_kinds constant text[] := ARRAY['session', 'portal-grant', 'account', 'marketing-consent', 'data-subject'];
    mac_re  text;
    lit     text;
    lit_re  text;
    quoted  text;
    changed bigint;
    total   bigint := 0;
BEGIN
    IF p_pseudonym IS NULL OR p_pseudonym !~ '^pseudonym:[0-9a-f]{16}$' THEN
        RAISE EXCEPTION 'foyer_audit_pseudonymise: % is not a pseudonym', coalesce(p_pseudonym, 'NULL')
            USING ERRCODE = '22023';
    END IF;

    IF p_mac_hex IS NOT NULL THEN
        IF p_mac_hex !~ '^[0-9a-f]{12}$' THEN
            RAISE EXCEPTION 'foyer_audit_pseudonymise: not twelve hex digits' USING ERRCODE = '22023';
        END IF;
        -- Every spelling a NAS sends: colons, dashes, Cisco's dots, none, either
        -- case. A separator is allowed between any two digits and the match may not
        -- be part of a longer hex run, so a token hash that happens to contain the
        -- digits is not rewritten.
        mac_re := '(?<![0-9a-f])' || array_to_string(regexp_split_to_array(p_mac_hex, ''), '[-:.]?') || '(?![0-9a-f])';
        UPDATE audit_log SET
            subject_id   = regexp_replace(subject_id, mac_re, p_pseudonym, 'gi'),
            subject_name = regexp_replace(subject_name, mac_re, p_pseudonym, 'gi'),
            params       = regexp_replace(params::text, mac_re, p_pseudonym, 'gi')::jsonb
        WHERE tenant_id = foyer_current_tenant()
          AND (subject_id ~* mac_re OR subject_name ~* mac_re OR params::text ~* mac_re);
        GET DIAGNOSTICS changed = ROW_COUNT;
        total := total + changed;
    END IF;

    FOREACH lit IN ARRAY coalesce(p_literals, '{}'::text[]) LOOP
        IF lit IS NULL OR length(lit) < 3 THEN
            RAISE EXCEPTION 'foyer_audit_pseudonymise: an identifier shorter than three characters' USING ERRCODE = '22023';
        END IF;
        -- A value inside params is replaced only where it is a whole JSON string (the
        -- quotes are part of the match), case-insensitively; subject_id and
        -- subject_name only where they are the identifier exactly.
        quoted := to_jsonb(lit)::text;
        lit_re := regexp_replace(quoted, '([.^$*+?()\[\]{}|\\-])', '\\\1', 'g');
        UPDATE audit_log SET
            subject_id   = CASE WHEN lower(subject_id) = lower(lit) THEN p_pseudonym ELSE subject_id END,
            subject_name = CASE WHEN lower(subject_name) = lower(lit) THEN p_pseudonym ELSE subject_name END,
            params       = regexp_replace(params::text, lit_re, to_jsonb(p_pseudonym)::text, 'gi')::jsonb
        WHERE tenant_id = foyer_current_tenant()
          AND subject_kind = ANY(guest_kinds)
          AND (lower(subject_id) = lower(lit) OR lower(subject_name) = lower(lit) OR params::text ~* lit_re);
        GET DIAGNOSTICS changed = ROW_COUNT;
        total := total + changed;
    END LOOP;
    RETURN total;
END
$$;
-- +goose StatementEnd

ALTER FUNCTION foyer_audit_pseudonymise(text, text[], text) OWNER TO foyer_audit_redactor;
REVOKE ALL ON FUNCTION foyer_audit_pseudonymise(text, text[], text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION foyer_audit_pseudonymise(text, text[], text) TO foyer_app;

COMMENT ON FUNCTION foyer_audit_pseudonymise(text, text[], text) IS
    'Replaces one erased data subject''s identifiers in audit_log with a keyed pseudonym '
    '(00052). The only UPDATE path on audit_log; foyer_app holds EXECUTE, '
    'not UPDATE.';

-- +goose Down

DROP FUNCTION foyer_audit_pseudonymise(text, text[], text);
DROP POLICY audit_log_pseudonymise ON audit_log;
REVOKE ALL ON audit_log FROM foyer_audit_redactor;
REVOKE USAGE ON SCHEMA public FROM foyer_audit_redactor;
-- The role is cluster-level and deliberately not dropped, as in 00001.
