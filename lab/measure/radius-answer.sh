# shellcheck shell=bash
# radius-answer.sh — who answered a RADIUS request, read from `radclient -x`.
#
# Sourced, not run. login-flood.sh asks one question of every probe: did the
# POLICY API answer it? Counting any `Received Access-*` as yes is wrong: the
# generated virtual server answers an Access-Reject of its own when the Policy
# API is down — `Reply-Message = "policy engine unavailable"` — so an outage
# during the flood would be reported as the Policy API answering. The strings are the generated tree's
# (internal/raddb/templates/default.site.tmpl); TestRadiusAnswerClassification
# in this directory holds each class against a radclient transcript.
#
# radius_answer prints exactly one word for the transcript on stdin:
#
#   policy       the Policy API decided: an Access-Accept that is not a
#                fail-open grant, or the tree's "denied by policy" reject
#   unavailable  the tree's own reject for a Policy API that did not answer
#   degraded     a fail-open grant (Class Foyer:degraded), minted blind
#   unexplained  an answer that says neither — never counted as the Policy API's
#   none         no answer at all
radius_answer() {
	local out
	out=$(cat)
	if ! grep -q 'Received Access-' <<<"$out"; then
		echo none
	elif grep -Eq 'Foyer:degraded|0x466f7965723a6465677261646564' <<<"$out"; then
		echo degraded
	elif grep -q 'Reply-Message = "policy engine unavailable"' <<<"$out"; then
		echo unavailable
	elif grep -q 'Received Access-Accept' <<<"$out"; then
		echo policy
	elif grep -q 'Reply-Message = "denied by policy"' <<<"$out"; then
		echo policy
	else
		echo unexplained
	fi
}
