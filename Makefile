.PHONY: all test typecheck build build-go web-embed generate generate-check web-deps diff-api fmt-check lab-up lab-down lab-reset lab-certs lab-kek lab-raddb lab-nas-auth lab-nas-acct lab-nas-sync lab-test check-all ci ci-fast ci-integration vulncheck supply-verify supply-pin clean

all: build test

# ─────────────────────────────────────────────────────── the revision ──
#
# The commit the binaries below are built from, stamped with `-X` exactly as
# deploy/Dockerfile.foyer stamps an image (internal/version). Without it the
# toolchain's own `vcs.revision` answers, and in a linked git worktree that is
# the main repository's HEAD, not the worktree's, and its `vcs.modified` misses
# modified files there too. So the Makefile asks git in the directory it runs in,
# which is this checkout, and appends `+dirty` itself when a tracked file differs
# from HEAD. No git, or not a repository: empty, and the binary falls back to the
# toolchain and then to `unknown`, as before. `make REVISION=… build` overrides.
# `TestMakeStampsTheCommitOfTheCheckoutItRunsIn` (internal/version) holds it.
REVISION := $(shell r=$$(git rev-parse --short HEAD 2>/dev/null) && { git diff --quiet HEAD -- 2>/dev/null && echo "$$r" || echo "$$r+dirty"; })
VERSION_LDFLAGS = -X github.com/kiennt048/foyer/internal/version.revision=$(REVISION)

# Run all unit tests
test:
	go test -v ./...
	cd web && pnpm -r test

# Frontend typecheck
typecheck:
	cd web && pnpm -r typecheck

# Go binaries only.
#
# Split out because `lab-raddb` needs `foyerctl` and nothing else, and depending
# on the full `build` made generating a FreeRADIUS configuration tree require
# pnpm and an installed `web/node_modules`. A lab target that cannot run on a
# machine which has never built the frontends is a lab target that does not run.
#
# **It embeds whatever is in the two directories below at the moment it runs**,
# which on a fresh clone is the `.gitkeep` placeholder and therefore no UI at all.
# That is deliberate and it is what keeps this target usable without pnpm; the
# binary says so at runtime rather than pretending (internal/webui answers 503
# with `X-Foyer-UI: absent`). `make build` is the one that produces a binary with
# a frontend in it.
build-go:
	go build -ldflags "$(VERSION_LDFLAGS)" -o bin/foyer ./cmd/foyer
	go build -ldflags "$(VERSION_LDFLAGS)" -o bin/foyerctl ./cmd/foyerctl

# ────────────────────────────────────────────── the embedded frontends ──
#
# The Go binary serves the portal and the console out of an embed.FS
# (internal/webui), so these two directories are as much build input as
# `internal/api/gen` is build output. A binary built with nothing in them serves
# a redirect loop and a 404.
#
# **The order below is the whole point.** `//go:embed` reads the directory when
# the package is COMPILED, so building Go before the frontends embeds the
# previous run's output, or the placeholder, and reports success. That is not a
# slow build, it is a wrong binary, and nothing downstream can tell.
WEBUI_DIRS = internal/webui/portal-dist internal/webui/admin-dist

# A DELETE-sync, not a copy over the top.
#
# Both frontends emit content-hashed filenames, so copying onto a populated
# directory accumulates every asset ever built and embeds all of them — the
# binary grows, and `internal/webui`'s own idea of what it is serving stops
# matching `web/*/dist`. `find -delete` is depth-first, so subdirectories are
# emptied before they are removed.
#
# `.gitkeep` is spared BY NAME because it is a tracked file: it is what makes
# `//go:embed all:portal-dist` compile in a clone where nobody has run pnpm, and
# deleting it would leave a dirty tree and a build that fails on the next fresh
# checkout. It is owned by internal/webui, not by this file.
web-embed:
	cd web && pnpm -F @foyer/portal build
	cd web && pnpm -F @foyer/admin build
	@for d in $(WEBUI_DIRS); do \
		mkdir -p $$d; \
		find $$d -mindepth 1 ! -name .gitkeep -delete; \
	done
	cp -R web/portal/dist/. internal/webui/portal-dist/
	cp -R web/admin/dist/. internal/webui/admin-dist/
	@for d in $(WEBUI_DIRS); do \
		n=$$(find $$d -type f ! -name .gitkeep | wc -l | tr -d ' '); \
		if [ "$$n" -eq 0 ]; then \
			echo "  $$d is empty after the sync — the build produced nothing"; exit 1; \
		fi; \
		echo "  embedded $$n files into $$d"; \
	done

# Build the Go binaries with the frontends embedded in them.
#
# Sequenced with two $(MAKE) calls rather than `build: web-embed build-go`,
# because prerequisites of one target have no order under `make -j` and this
# pair does. The same idiom is used by `lab-test` below.
build:
	$(MAKE) web-embed
	$(MAKE) build-go

# ─────────────────────────────────────────────────── contract codegen ──
#
# `internal/api/gen/server.gen.go` is build output in the same sense `raddb/`
# is: it is generated from `web/contract/openapi.yaml` and never hand-edited.
# Generating the interface is what extends the mock's rule — "a handler for a
# nonexistent operation does not compile" — to the Go side, so contract drift
# becomes a build failure instead of an integration surprise.
generate:
	go generate ./internal/api/gen/
	@go run ./tools/codegen/vendorevidence

# The same, as a gate. A `make generate` that produces a diff means somebody
# edited the generated file or changed the contract without regenerating, and
# either way the build output no longer matches its source. Non-zero exit is
# what makes that a CI failure rather than a thing a reviewer might notice.
generate-check: generate
	@if [ -n "$$(git status --porcelain -- internal/api/gen)" ]; then \
		echo ""; \
		echo "  make generate changed internal/api/gen — the checked-in server does not"; \
		echo "  match web/contract/openapi.yaml. Commit the regenerated file."; \
		echo ""; \
		git --no-pager diff --stat -- internal/api/gen; \
		exit 1; \
	fi
	@if [ -n "$$(git status --porcelain -- web/fixtures/src/generated)" ]; then \
		echo ""; \
		echo "  make generate changed web/fixtures/src/generated — the mock's vendor evidence"; \
		echo "  no longer matches internal/nas. That file is generated, never hand-edited:"; \
		echo "  a stale copy is a fake asserting a Cisco encoding is documented, in a suite"; \
		echo "  whose whole premise is that nothing here has met a Cisco device."; \
		echo ""; \
		git --no-pager diff --stat -- web/fixtures/src/generated; \
		exit 1; \
	fi
	@echo "  internal/api/gen matches the contract"
	@echo "  web/fixtures/src/generated matches internal/nas"

# ──────────────────────────────────────────── the differential harness ──
#
# `web/mock` is the behavioural oracle for `internal/api`: stateful,
# schema-validated behaviour the frontends are built and tested against. This
# drives one scenario into it and into the Go server and compares the answers.
#
# The mock is started on a private port with `FOYER_MOCK_LIVE=0`, which stops
# its simulation clock. Without that it advances counters and flips a NAS
# unreachable every 20 seconds, and a comparison would be racing it.
FOYER_DIFF_PORT ?= 5179

# The mock verifies the sign-in password, and generates a random
# one per process when this is unset -- at which point the differ cannot sign in and
# `make diff-api` dies at step one. It must be the differ's own `harnessPassword`
# (tools/codegen/differ/main.go), and the differ prints this exact variable and value
# in its sign-in error so the failure names its own cure.
#
# Not a credential and not covered by docs/rules/security.md §7: the mock is a development
# oracle that is never deployed, the value says so in its own text, and the same
# string is already a const in the differ's source. A generated secret here would
# have to be handed to the differ anyway, which is the thing being avoided.
FOYER_DIFF_MOCK_PASSWORD ?= harness-only-not-a-deployment-value

# The `web/node_modules` guard, lifted out of `diff-api` rather than copied into
# `ci-integration` beside it. The message is the one `diff-api` already carried;
# there is one of it. Two copies of a precondition is a second source of truth
# for a fact something else owns, and it drifts the day the install command
# changes.
web-deps:
	@if [ ! -d web/node_modules ]; then \
		echo "  web dependencies are not installed:  cd web && pnpm install"; exit 1; \
	fi

# The preflight below FAILS OPEN where `lsof` is not installed: the `if` is simply
# false and the target proceeds. That is stated rather than hidden — it is a
# guard against an accident, not a security boundary, and its limits are
# documented here so nobody mistakes it for one.
diff-api: web-deps
	@if lsof -ti :$(FOYER_DIFF_PORT) > /dev/null 2>&1; then \
		echo ""; \
		echo "  something is already listening on $(FOYER_DIFF_PORT), and this target does not"; \
		echo "  own it. The mock started below would fail to bind, the wait loop would"; \
		echo "  find the squatter's /health, and the differ would compare this tree's Go"; \
		echo "  server against a process you did not start — reporting on code this run"; \
		echo "  did not build. A leftover mock from an earlier run is the usual cause, and"; \
		echo "  only the differ's dirty-mock guard would notice it — a freshly started"; \
		echo "  foreign mock would not trip even that."; \
		echo ""; \
		echo "  This is web/e2e's hardcoded-port hazard on a second port. Stop it, or:"; \
		echo "    make diff-api FOYER_DIFF_PORT=<spare>"; \
		echo ""; \
		exit 1; \
	fi
	@PORT=$(FOYER_DIFF_PORT) FOYER_MOCK_LIVE=0 \
		FOYER_MOCK_ADMIN_PASSWORD=$(FOYER_DIFF_MOCK_PASSWORD) \
		pnpm --dir web -F @foyer/mock start > /tmp/foyer-diff-mock.log 2>&1 & \
	echo $$! > /tmp/foyer-diff-mock.pid; \
	for i in $$(seq 1 40); do \
		curl -sf http://127.0.0.1:$(FOYER_DIFF_PORT)/health > /dev/null && break; \
		sleep 0.25; \
	done; \
	go run ./tools/codegen/differ -mock http://127.0.0.1:$(FOYER_DIFF_PORT)/api/v0; \
	status=$$?; \
	pkill -f "PORT=$(FOYER_DIFF_PORT)" 2>/dev/null || true; \
	kill $$(cat /tmp/foyer-diff-mock.pid) 2>/dev/null || true; \
	rm -f /tmp/foyer-diff-mock.pid; \
	exit $$status

# ────────────────────────────────────────────────────────── formatting ──
#
# Scope the path list. A bare `gofmt -l .` also walks untracked directories
# under the repository root.
GOFMT_PATHS = ./internal ./cmd ./lab ./tools

# `gofmt -l` prints the offending files and **exits 0**: it has no failure mode
# of its own. A bare `gofmt -l` line inside `check-all` would be decorative — a
# deliberately misformatted file in `internal/` is printed by name and the target
# still exits 0. A mechanism that looks like a check and is not; the fix is to
# capture the output and decide.
fmt-check:
	@out="$$(gofmt -l $(GOFMT_PATHS))"; \
	if [ -n "$$out" ]; then \
		echo ""; \
		echo "  gofmt: these files are not formatted:"; \
		echo "$$out" | sed 's/^/    /'; \
		echo ""; \
		echo "  run:  gofmt -w $(GOFMT_PATHS)"; \
		echo ""; \
		exit 1; \
	fi
	@echo "  gofmt: $(GOFMT_PATHS) are formatted"

# The lab's PostgreSQL password. It is the one in lab/docker-compose.yml, and it
# is here because the generator **refuses an empty password** — deliberately:
# `read_clients = yes` reads the client table before the first packet, so a
# database FreeRADIUS cannot reach is not a degraded server, it is a server that
# does not start. `make lab-raddb` fails outright without this.
#
# A real install passes its own: `make lab-raddb FOYER_POSTGRES_PASSWORD=...`, or
# exports it. There is no default outside the lab and there must not be one.
#
# It is FreeRADIUS's OWN password (migration 00019): the
# generated tree logs in as `foyer_radius` (foyerctl's --pg-user default), not as
# the superuser, and lab/docker-compose.yml's migrate one-shot sets this same
# value as FOYER_RADIUS_DB_PASSWORD. The two must agree or FreeRADIUS does not
# start; internal/store's nas-projection lab test pins the same literal.
FOYER_POSTGRES_PASSWORD ?= foyer_lab_secret_radius

# The lab's CA, Policy API certificate and the client certificate FreeRADIUS
# presents to it. Idempotent; generates into lab/.state, which is gitignored.
#
# The lab exercises the mTLS boundary rather than lifting it. lab/make-policy-certs.sh
# carries the reasoning; the short version is that internal/raddb's rest module is
# only ever read by a real FreeRADIUS here, and the item names inside it are
# measured rather than guessable.
lab-certs:
	bash lab/make-policy-certs.sh
	@# The lab's freeradius container mounts this key directly and radiusd reads
	@# it as `freerad`; a bind mount on a Linux host keeps the developer's
	@# uid and the 0600 above. A throwaway lab key; the deployment grants its own
	@# to gid 1812 instead (deploy/issue-radius-cert.sh).
	chmod 0644 lab/.state/policy-tls/radius.key

# The lab's key-encryption key: FOYER_KEK, for RADIUS shared secrets, controller
# credentials and NT hashes. Idempotent; generated into lab/.state, which
# .gitignore already covers, and reused by every later `make lab-up`.
#
# **Why it is generated here and not committed, in one sentence: a key in the
# file is a key every deployment that clones this repository shares**, which is
# the same failure as a CA shipped in an image (docs/rules/security.md §7). There is
# therefore no default *value* here — there is a generator, and the value it
# produces lands on one machine and dies with it. `deploy/first-boot.sh` does
# exactly this for a real install, from `secrets/kek`.
#
# **Why it exists at all: without one `make lab-up` comes up quietly half a
# lab.** With FOYER_KEK unset the control plane warns at start-up that
# "registering a controller, storing an NT hash and rotating a shared secret
# will be refused", and then goes on to work perfectly: the
# FreeRADIUS path, the policy engine and accounting are all unaffected, so
# nothing is red. What IS refused is the first thing an operator does —
# registering a device, because `SharedSecretRepo.Create` seals the secret
# (internal/store/secrets.go) and `Keyring.Seal` returns ErrNoKeyring on a nil
# keyring. An operator whose first two clicks do nothing, on a lab whose
# console looks healthy, is the failure this removes.
#
# Passing the key into the backend is `FOYER_KEK=<value> docker compose up` and
# nothing else: lab/docker-compose.yml already interpolates ${FOYER_KEK:-}, and
# the maintenance container is deliberately given no key (it unseals nothing
# and must not hold it).
#
# The format is `id:base64key`, the active key first, exactly as
# deploy/first-boot.sh writes it: the id is dated so a later rotation can say
# which key sealed which row without guessing.
#
# One trap worth naming, because it is the same one a deployment has: if you
# brought the lab up once with your own exported FOYER_KEK, keep exporting it.
# Stop, and `make lab-up` silently substitutes lab/.state/kek, and every row
# sealed under yours becomes ciphertext. `foyerctl doctor` names the key it is
# reading; the remedy is to export the same value again, not to reset the
# database.
lab-kek:
	@command -v openssl > /dev/null 2>&1 || { \
		echo ""; \
		echo "  openssl is not on PATH, so the lab's key-encryption key cannot be generated."; \
		echo "  The lab refuses to run without one (see the note above), so this is the fix:"; \
		echo "    install openssl, or export FOYER_KEK='<id>:<base64 32 bytes>' yourself"; \
		echo ""; \
		exit 1; }
	@mkdir -p lab/.state
	@if [ -s lab/.state/kek ]; then \
		echo "  lab key-encryption key: lab/.state/kek (kept from the first lab-up)"; \
	else \
		umask 077; printf '%s:%s\n' "$$(date -u +%Y-%m)" "$$(openssl rand -base64 32)" > lab/.state/kek; \
		chmod 0600 lab/.state/kek; \
		echo "  lab key-encryption key: generated into lab/.state/kek on this machine."; \
		echo "  It is never committed (.gitignore covers lab/.state) and never shared with a"; \
		echo "  deployment, whose own key comes from deploy/first-boot.sh."; \
	fi

# Generate lab FreeRADIUS configuration.
#
# The policy URL is https and the generator refuses to write half of that pair in
# either direction, which is what stops a tree requiring a certificate from being
# generated before one exists — the ordering hazard.
#
# The three files FreeRADIUS reads live INSIDE the generated tree, at
# ${confdir}/certs — which is where radiusd.conf's own certdir points, and which
# is a directory the generator already creates. That is not tidiness:
#
#   * rlm_rest opens ca_file at INSTANTIATION, not on the first request. Measured
#     on 3.2.10: with the file absent the server says "Failed parsing
#     configuration item ca_file" and refuses to start. So every container that
#     mounts this tree must also see the material, or it does not come up — and
#     more than one does, including internal/store's nas-projection lab test.
#   * Putting it in the tree makes "mount lab/raddb at /etc/raddb" continue to be
#     the whole contract, rather than a contract plus a second mount nobody
#     remembers.
#
# A deployment does the opposite and passes /run/foyer/tls paths, because there
# the tree is a named volume and the material is in /opt/foyer/secrets. The paths
# are a parameter precisely so each context can answer this differently.
lab-raddb: build-go lab-certs
	FOYER_POSTGRES_PASSWORD=$(FOYER_POSTGRES_PASSWORD) ./bin/foyerctl generate-raddb --out lab/raddb \
		--policy-url=https://foyer-backend:8080/api/v1/aaa/authorize \
		--policy-client-cert=/etc/raddb/certs/radius.crt \
		--policy-client-key=/etc/raddb/certs/radius.key \
		--policy-server-ca=/etc/raddb/certs/ca.crt
	cp lab/.state/policy-tls/ca.crt lab/.state/policy-tls/radius.crt lab/.state/policy-tls/radius.key lab/raddb/certs/
	@# radiusd reads this key as `freerad`, and a bind mount
	@# on a Linux host keeps the developer's uid and the 0600 above. The lab's key
	@# is a throwaway from lab/.state; the deployment grants its own to gid 1812
	@# instead (deploy/issue-radius-cert.sh).
	chmod 0644 lab/raddb/certs/radius.key

# The nas-projection end-to-end: a real FreeRADIUS 3.2.10, real packets, and a
# RADIUS client written by `Store.NasProjection().Sync` rather than inserted by
# hand.
#
# It is a separate target rather than part of `go test ./...` because it builds an
# image and starts containers, and because a test that needs Docker has to be able
# to say it did not run. The test skips loudly without FOYER_LAB_E2E=1: an unrun
# assertion that says it is unrun is worth a great deal, and one that implies it
# passed is worth less than nothing.
FOYER_LAB_FR_IMAGE ?= foyer-nas-sync-freeradius:test

lab-nas-sync: lab-raddb
	docker build -f lab/Dockerfile.freeradius -t $(FOYER_LAB_FR_IMAGE) lab/
	FOYER_LAB_E2E=1 FOYER_LAB_FR_IMAGE=$(FOYER_LAB_FR_IMAGE) \
		go test ./internal/store/ -run TestNasProjectionAgainstRealFreeRADIUS -count=1 -v -timeout 15m

# ────────────────────────────────────────────── tier 2: the lab suites ──
#
# `README.md` and `CONTRIBUTING.md` tell contributors to run `make lab-test`.
#
# **It does not invoke the lab suites for the first time.** `lab/eap` and
# `lab/policy` are ordinary Go packages with no build tags — `go list ./...`
# names both — so `go test ./...` already runs them and `check-all` already
# runs them. What varies is not the invocation but **the environment**, and
# without it they skip:
#
#   * `requireDocker` in both packages skips on FOYER_LAB_SKIP, on docker
#     missing from PATH, and on a daemon that will not answer `docker info`.
#   * `internal/store`'s TestNasProjectionAgainstRealFreeRADIUS skips without
#     FOYER_LAB_E2E=1, and needs a generated tree at `lab/raddb` — which is
#     why this target runs `lab-raddb`, after the preflight rather than as a
#     prerequisite, so a machine without Docker is told so before it builds
#     binaries and a certificate authority it cannot use.
#
# So this target supplies that environment, and then **refuses to be green if
# the suites skipped anyway**. `go test` prints `ok` for a package whose tests
# all skipped; a tier-2 target that inherits that exit code is a mechanism that
# looks like a check and is not.
#
# It also resolves the FreeRADIUS image once, here, rather than letting each
# harness fall through to its own `docker build`. Two rules from those
# harnesses apply here as well:
#
#   * `docker image inspect` BEFORE `docker run`. `docker run` on an absent tag
#     tries to pull it, and a daemon whose registry path is unreachable hangs
#     there without erroring — no output and no timeout.
#   * Check the VERSION, never merely that a tag exists. A cached
#     `foyer-lab-freeradius:*` may be alpine's FreeRADIUS 3.0.27, a different
#     major dialect from the 3.2.x unlang `internal/raddb` emits, and it starts
#     and answers well enough that nobody notices.
#
# Candidates are the tags the two harnesses already look for, so this target
# and they agree about what is cached rather than each building their own.
LAB_FR_CANDIDATES ?= foyer-lab-freeradius:policy-harness foyer-lab-freeradius:eap-harness foyer-lab-freeradius:test foyer-nas-sync-freeradius:test

# The tests this target exists to run, asserted by name below. Naming
# them is the whole difference between "tier 2 ran" and "tier 2 reported ok".
LAB_REQUIRED_TESTS = TestEAPMethodsAgainstTheGeneratedConfiguration TestSIGSTOPOnThePolicyAPI TestNasProjectionAgainstRealFreeRADIUS TestRADIUSVerifiesTheCredential TestPolicyQuestionEncoding TestTheSupervisorInTheFreeRADIUSImage

lab-test:
	@if [ -n "$$FOYER_LAB_SKIP" ]; then \
		echo ""; \
		echo "  FOYER_LAB_SKIP is set, and this is the target whose only job is to run the"; \
		echo "  suites it disables. Unset it. Running tier 2 with it set would report"; \
		echo "  success for a run in which no packet was sent."; \
		echo ""; \
		exit 1; \
	fi
	@command -v docker > /dev/null 2>&1 || { \
		echo ""; \
		echo "  docker is not on PATH, so tier 2 cannot run here. lab/eap and lab/policy"; \
		echo "  each start a PostgreSQL and a real FreeRADIUS 3.2.x; there is no"; \
		echo "  in-process substitute, which is the point of the tier."; \
		echo ""; \
		exit 1; }
	@docker info --format '{{.ServerVersion}}' > /dev/null 2>&1 || { \
		echo ""; \
		echo "  docker is installed but the daemon does not answer \`docker info\`."; \
		echo "  Start it and re-run; tier 2 has nothing to say without it."; \
		echo ""; \
		exit 1; }
	$(MAKE) lab-raddb
	@set -e; \
	image="$$FOYER_LAB_FR_IMAGE"; \
	if [ -n "$$image" ]; then \
		echo "  FreeRADIUS image: $$image (from FOYER_LAB_FR_IMAGE)"; \
	else \
		for tag in $(LAB_FR_CANDIDATES); do \
			docker image inspect "$$tag" > /dev/null 2>&1 || continue; \
			v=$$(docker run --rm --entrypoint radiusd "$$tag" -v 2>&1 | head -1); \
			case "$$v" in \
				*"FreeRADIUS Version 3.2."*) \
					image="$$tag"; \
					echo "  FreeRADIUS image: $$tag"; \
					echo "  $$v"; \
					break ;; \
				*) echo "  skipping $$tag: $$v is not the 3.2.x dialect internal/raddb emits" ;; \
			esac; \
		done; \
	fi; \
	if [ -z "$$image" ]; then \
		echo "  no cached FreeRADIUS 3.2.x image; building lab/Dockerfile.freeradius"; \
		docker build -f lab/Dockerfile.freeradius -t foyer-lab-freeradius:tier2 lab/ || { \
			echo ""; \
			echo "  the build failed and nothing cached is the right dialect, so tier 2"; \
			echo "  cannot run on this machine. lab/Dockerfile.freeradius's own header"; \
			echo "  says what is needed: debian:sid is the only base carrying a"; \
			echo "  FreeRADIUS inside >= 3.2.9, < 3.3, and the InkBridge repository"; \
			echo "  publishes no arm64 index."; \
			echo ""; \
			exit 1; }; \
		image=foyer-lab-freeradius:tier2; \
	fi; \
	log=$$(mktemp -t foyer-lab-test); \
	: "The exit code is recorded with && / || rather than a following \`echo \$$?\`."; \
	: "\`set -e\` is in effect, so a failing \`go test\` in \`cmd; echo \$$?\` aborts the"; \
	: "subshell before the code is written — and the report then says NOT RUN for a"; \
	: "test that ran and failed. An AND-OR list is exempt from errexit and always writes."; \
	( FOYER_LAB_FR_IMAGE="$$image" FOYER_LAB_E2E=1 \
		go test -count=1 -v -timeout 30m ./lab/... \
		&& echo 0 > "$$log.rc1" || echo 1 > "$$log.rc1" ) 2>&1 | tee "$$log"; \
	( FOYER_LAB_FR_IMAGE="$$image" FOYER_LAB_E2E=1 \
		go test -count=1 -v -timeout 20m ./internal/store/ ./internal/raddb/ \
			-run '^(TestNasProjectionAgainstRealFreeRADIUS|TestTheSupervisorInTheFreeRADIUSImage)$$' \
		&& echo 0 > "$$log.rc2" || echo 1 > "$$log.rc2" ) 2>&1 | tee -a "$$log"; \
	status=0; \
	[ "$$(cat $$log.rc1)" = "0" ] || status=1; \
	[ "$$(cat $$log.rc2)" = "0" ] || status=1; \
	echo ""; \
	for t in $(LAB_REQUIRED_TESTS); do \
		if grep -q "^--- PASS: $$t " "$$log"; then \
			echo "  ran and passed: $$t"; \
		else \
			echo "  NOT RUN: $$t did not report PASS — it skipped, or it never ran."; \
			echo "           A skipped tier-2 suite is UNPROVEN, not green."; \
			status=1; \
		fi; \
	done; \
	rm -f "$$log" "$$log.rc1" "$$log.rc2"; \
	echo ""; \
	if [ $$status -ne 0 ]; then \
		echo "  tier 2 FAILED"; echo ""; exit 1; \
	fi; \
	echo "  tier 2 passed against $$image"; \
	echo ""

# Launch Docker test lab.
#
# `--build`, always: without it compose reuses whatever foyer-lab-backend:dev and
# FreeRADIUS image are cached, which may have been built from different source,
# so lab evidence could silently come from a tree other than this one. Docker's
# layer cache makes an unchanged rebuild cheap.
#
# No `lab-raddb` here: the control plane generates FreeRADIUS's tree from what
# the console stored and hands it to the supervisor in the freeradius container
# (internal/raddb/handoff.go). `lab-raddb` still writes the tracked lab/raddb the
# tier-2 harnesses start their own FreeRADIUS from.
#
# A failed `up` names the one cause it cannot tell from the others: a lab database
# whose applied migrations do not match this tree's. goose records versions by
# NUMBER, so such a database either has a gap below its latest version, which
# goose refuses ("missing migrations"), or has a number applied whose file is now
# a different migration, which goose skips — and the failure surfaces later as a
# relation or column that is not there. The lab's data is disposable;
# `make lab-reset` is the answer to both.
#
# FOYER_REV is the lab image's REVISION build argument (lab/docker-compose.yml),
# the same stamp `build-go` passes: the build context carries no `.git`
# (.dockerignore), so without it every lab binary says `revision unknown`.
lab-up: lab-certs lab-kek
	@# `$${FOYER_KEK:-…}` so an operator's own exported key still wins: this is a
	@# default, not an override. See lab-kek above for why there is no committed
	@# value and why the lab must not come up without one.
	@FOYER_KEK="$${FOYER_KEK:-$$(cat lab/.state/kek)}" FOYER_REV="$(REVISION)" \
		docker compose -f lab/docker-compose.yml up -d --build || { \
		echo ""; \
		echo "  lab-up failed. If migrate said \"missing migrations\", or anything failed on a relation"; \
		echo "  or column that should exist, the lab database does not match this tree's migrations:"; \
		echo "  \`make lab-reset\` drops it and starts again (lab/README.md says why)."; \
		echo "  Otherwise: docker compose -f lab/docker-compose.yml logs migrate foyer-backend"; \
		exit 1; }

# Stop Docker test lab
lab-down:
	docker compose -f lab/docker-compose.yml down

# Throw the lab's database away and bring the lab back on a fresh one.
#
# `down -v` removes the PostgreSQL volume, which plain `down` leaves behind. lab/.state
# (the lab's generated certificates) is kept: it is regenerated on demand by
# lab-raddb and holds nothing a schema depends on. lab/README.md says when to reach
# for this.
#
# That includes the key-encryption key, and it must: regenerating it on a reset
# would make every row sealed under the old one unreadable, which is the same
# trap as rotating FOYER_KEK on a deployment (deploy/README.md). Nothing in a
# fresh database is sealed, so there is nothing to lose — and a developer who
# wants a different key deletes lab/.state/kek, not this rule.
lab-reset:
	docker compose -f lab/docker-compose.yml down -v --remove-orphans
	$(MAKE) lab-up

# ─────────────────────────────────────────────── be a NAS against the lab ──
#
# The one thing the lab exists for that nothing else does: a real
# Access-Request, sent the way a device sends one, answered by the real
# FreeRADIUS 3.2.10 and decided by the real Policy API over the real mTLS
# boundary. lab/nas.sh carries the reasoning; the short version is that it takes
# the shared secret from `nas` as foyer_radius — the login FreeRADIUS itself
# uses — so nobody reads it off a screen and retypes it, which is how you get
# `invalid Message-Authenticator! (Shared secret is incorrect.)` from a product
# that is working.
#
# `lab-nas-auth` sends BOTH probes — a valid voucher code, which must be
# accepted, and a code in no batch, which must be refused. One command, both
# answers, because a tool that has only ever seen accepts has proved nothing.
lab-nas-auth:
	bash lab/nas.sh auth

# The follow-on: the same allow, then Accounting-Start and -Stop for that
# session, and the row FreeRADIUS wrote to radacct. Foyer never writes radacct
# (docs/rules/database.md), so that row is the evidence the two halves met.
lab-nas-acct:
	bash lab/nas.sh acct

# Run the complete verification checklist
# `go mod tidy -diff` because an untidy go.mod — a module imported directly and
# required as // indirect — is invisible to every other step here. `-diff` changes
# nothing and exits 1 on any difference.
check-all:
	go build ./... && go vet ./...
	$(MAKE) fmt-check
	go mod tidy -diff
	$(MAKE) generate-check
	go test -v ./...
# `-race` is part of the definition of done for every backend change: a race
# detector that nobody runs finds nothing. It is a second full pass on
# purpose: the race build is a different binary, and a race is a property of the
# schedule rather than of the assertions, so it is not a superset of the run
# above.
	go test -race ./...
	cd web && pnpm -r typecheck
	cd web && pnpm -r test
	cd web && pnpm -F @foyer/portal build && node tools/check-budget.mjs
	cd web && pnpm -F @foyer/admin build && node tools/check-admin-budget.mjs
# The frontends are built by the two lines above; this is what asks whether they
# reach the binary. Without it nothing in `make ci` asks whether the thing the
# control plane actually serves exists, and a deployment can answer a redirect
# loop on the portal and 404 on the console with every gate green.
#
# It sits HERE, after the budget checks, and not beside `go build ./...` at the top,
# because putting it there would make the Go unit suite require pnpm and an
# installed `web/node_modules`. `build-go` exists precisely to avoid that coupling
# for `lab-raddb`, and buying this gate at the price of that one would be a poor
# trade. The consequence is stated rather than hidden: `go test ./...` above runs
# against whatever was embedded when it started, and this line is what says the
# tree is real afterwards.
#
# `$(MAKE) web-embed` repeats the two builds above. That is about a second, and the
# alternative — splitting the sync out of `web-embed` so this target can call half
# of it — would give the repository two ways to populate those directories and one
# of them would drift.
	$(MAKE) web-embed
# **This gate is unconditional.** A guard like `if ls internal/webui/*.go` would be
# self-RESURRECTING: rename the package and this recipe falls into a friendly
# else-branch and exits 0 having checked nothing. Here, deploy/Dockerfile.foyer and
# lab/Dockerfile.backend alike fail loudly if the package moves.
#
# The package is named and the test is NOT: `go test -run SomeName` exits 0 when
# the name matches nothing, so pinning a test name here would turn a rename into a
# silently green gate.
#
# And `-v` plus a required `--- PASS:` is there because naming no test is not enough:
# **go test exits 0 for a package with no test files, and for one whose tests all
# skip.** Deleting internal/webui/webui_test.go would otherwise leave this line
# reporting success for a run in which nothing was asserted, which is the shape
# lab-test guards against by name further down this file.
	@echo "  gate: FOYER_REQUIRE_WEBUI=1 go test ./internal/webui/"; \
	FOYER_REQUIRE_WEBUI=1 go test -v ./internal/webui/ > /tmp/foyer-webui-gate.log 2>&1 || { cat /tmp/foyer-webui-gate.log; exit 1; }; \
	grep -q '^--- PASS: ' /tmp/foyer-webui-gate.log || { cat /tmp/foyer-webui-gate.log; echo "  no test in internal/webui reported PASS. go test exits 0 for a package"; echo "  with no test files and for one whose tests all skip, so this gate would"; echo "  have passed vacuously."; exit 1; }; \
	tail -1 /tmp/foyer-webui-gate.log
	cd web && node tools/check-tokens.mjs
# The census of three-valued booleans. `web/` has no linter, deliberately, and
# this is that decision's expiry condition made evaluable: it fails when the
# contract grows an optional boolean nobody has classified, or when the
# locks that make a small class safe are deleted underneath it. Needs no build, so
# it sits with the token lint.
	cd web && node tools/check-tristate.mjs
# Development-tooling checks, when present.

# ─────────────────────────────────────────────────────────── the gate ──
#
# `make ci` is the gate, and it runs on a developer's machine exactly as it runs
# in CI. `.github/workflows/ci.yml` is the thin caller and this is the thing being
# called — not the other way round.
#
# Layered so a contributor can run the fast half on its own.

# The fast half: everything that needs no Docker, no container and no second
# server. This is `check-all` and nothing else — a wrapper that adds steps
# would make `check-all` and `ci-fast` two different gates with one name each,
# and the next person to cite one would mean the other.
ci-fast:
	$(MAKE) check-all

# The slow half.
#
# **What is in it:** `diff-api` (the differential harness against `web/mock`)
# and `lab-test` (tier 2 — real FreeRADIUS, real packets).
#
# **What is NOT in it, and why: the blue/green gates.** `lab/blue-green/gates.sh`
# drives `radiusd -XC`, a second `radiusd` on alternate ports, `radclient` and
# `eapol_test`. Those binaries are not on this host and are not meant to be —
# the script ships inside `lab/Dockerfile.freeradius` and `deploy/Dockerfile.freeradius`
# precisely because the control plane and radiusd are separate containers, and
# gates 2–4 need a live tree with a probe client and a shared secret. Driving it
# from here would mean inventing that environment, and an invented one is an
# input the harness cannot be wrong about (docs/rules/testing.md).
#
# The part of it that CAN be checked without a lab already is, and already sits
# in `ci-fast`: `internal/raddb/gates_agreement_test.go` reads `gates.sh` and
# asserts it names the same gates in the same order as the Go `Applier`, and
# that it refuses to run without `PROBE_SECRET`. That is an ordinary Go test
# inside `go test ./...`. What is missing is an execution of gates 2–4, and it
# is missing because there is no radiusd here — not because nobody wrote it.
#
# **Both halves run even when the first fails.** A `$(MAKE) diff-api` followed
# by `$(MAKE) lab-test` would stop at the first, so a red `diff-api` would mean
# tier 2 never ran at all — which is how a gate becomes an unrun mechanism while
# looking like it is enforcing something.
#
# `diff-api` exits non-zero on undeclared differences between the mock and the
# Go server. An undeclared difference is a finding about `internal/apistore`, not
# harness noise, and `make ci` stays red and says why rather than going green
# because the failing step was excluded.
#
# **There is deliberately no baseline of expected differences here.** A count or
# a list of the known-undeclared fields, typed into this file, would be a second
# copy of something the differ already computes — and docs/rules/testing.md
# records what that costs in both directions: it goes red the day somebody
# repairs one of them, reporting the repair as a regression with the same exit
# code as a real drift. The differ's own declaration mechanism is where a
# difference gets declared; this file does not get a vote.
ci-integration: web-deps
	@status=0; diff_rc=0; lab_rc=0; \
	echo ""; \
	echo "  ── ci-integration 1/2: make diff-api ──────────────────────"; \
	echo ""; \
	$(MAKE) diff-api || { diff_rc=1; status=1; }; \
	echo ""; \
	echo "  ── ci-integration 2/2: make lab-test ──────────────────────"; \
	echo ""; \
	$(MAKE) lab-test || { lab_rc=1; status=1; }; \
	echo ""; \
	echo "  ── ci-integration summary ─────────────────────────────────"; \
	[ $$diff_rc -eq 0 ] && echo "    diff-api  passed" || echo "    diff-api  FAILED"; \
	[ $$lab_rc  -eq 0 ] && echo "    lab-test  passed" || echo "    lab-test  FAILED"; \
	if [ $$diff_rc -ne 0 ]; then \
		echo ""; \
		echo "    diff-api is not green. Read the differ's own \"undeclared"; \
		echo "    differences\" block above: each line is a field where the Go"; \
		echo "    server and web/mock disagree. Check it against the open issues"; \
		echo "    before concluding anything has drifted — a difference that is"; \
		echo "    not already known is new, and is the finding."; \
	fi; \
	echo ""; \
	exit $$status

# ── vulncheck: known vulnerabilities the code actually reaches ──
#
# govulncheck reports the known vulnerabilities this code actually calls, in the
# standard library and in its dependencies, which nothing else here can notice.
#
# `go run …@version`, not a go.mod dependency and not `@latest`:
#
#   * go.mod is frozen, and a scanner is not something the product links.
#   * `@latest` makes the gate a different program on every run, and a red that
#     appears because the scanner moved is indistinguishable from one that
#     appears because the tree did. Bump GOVULNCHECK_VERSION on purpose.
#
# What still moves between runs is the vulnerability database, and that is the
# point of the gate: it goes red the day a new advisory lands for something this
# code calls, with no change to the tree. That is why it needs the network, and
# why it is NOT in `check-all` — the fast half runs offline, and the patched-floor
# half of this story (go.mod's `go` line, the builder images, the alpine branch)
# is asserted offline by tools/supplycheck inside `go test ./...` instead.
#
# It fails only on vulnerabilities the code CALLS (govulncheck exits 3). A module
# that merely contains a vulnerable package the code never imports is printed and
# does not fail — the default mode, and the one golang/govulncheck-action runs.
GOVULNCHECK_VERSION ?= v1.8.0

vulncheck:
	go run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) ./...

# ── supply-verify: the bytes, not the tag ──
#
# tools/supplycheck's tests read the tree and cannot tell whether the digest beside
# `golang:1.26.8-alpine3.24` is that image; Docker ignores the tag when a digest is
# present, so an older builder under an unchanged tag passed everything offline. This
# asks the registry that every pinned tag names its pinned digest today (with a
# linux/amd64 image in it), and runs govulncheck -mode=binary on the binaries
# deploy/Dockerfile.foyer actually builds. Networked, like vulncheck, and for the
# same reason it can go red on a tree nobody touched: a tag that moved is a base
# missing the fixes it now carries. `make supply-pin` is the remedy — Renovate's
# pinDigests, as a target — and its diff is reviewed and committed like any other.
# tools/supplycheck/network.sh has the rest.
supply-verify:
	GOVULNCHECK_VERSION=$(GOVULNCHECK_VERSION) bash tools/supplycheck/network.sh

supply-pin:
	bash tools/supplycheck/network.sh --update

# Both halves. The fast half first, and `make` stops here if it fails — there is
# no point spending twenty minutes of containers on a tree that does not compile.
#
# `vulncheck` and `supply-verify` sit between them rather than after `ci-integration`, because
# `ci-integration` can be red (see above) and `make` stops at the first failure:
# placed last, the scan would never run.
ci:
	$(MAKE) ci-fast
	$(MAKE) vulncheck
	$(MAKE) supply-verify
	$(MAKE) ci-integration

# `clean` removes build output, and the embedded frontends are build output —
# a `clean` that leaves several megabytes of hashed assets behind, to be silently
# compiled into the next binary, is not one.
#
# The `.gitkeep` in each directory survives, for the reason `web-embed` states:
# it is tracked, and it is what makes the embed compile on a tree where nobody
# has run pnpm. So `make clean && go build ./...` still works, and produces a
# binary that says it has no UI instead of one that will not link.
clean:
	rm -rf bin/
	@for d in $(WEBUI_DIRS); do \
		[ -d $$d ] || continue; \
		find $$d -mindepth 1 ! -name .gitkeep -delete; \
		echo "  cleaned $$d"; \
	done
