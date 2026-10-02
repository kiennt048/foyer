package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/kiennt048/foyer/internal/config"
	"github.com/kiennt048/foyer/internal/raddb"
	"github.com/kiennt048/foyer/internal/store"
)

func init() {
	Register(&Command{
		Name:    "generate-raddb",
		Usage:   "--out <dir>",
		Summary: "Generate FreeRADIUS 3.2.x configuration",
		Run:     runGenerateRaddb,
	})
}

func runGenerateRaddb(args []string) error {
	fs := flag.NewFlagSet("generate-raddb", flag.ExitOnError)
	outDir := fs.String("out", "lab/raddb", "Output directory for generated FreeRADIUS configuration")
	policyURL := fs.String("policy-url", "http://foyer-backend:8080/api/v1/aaa/authorize", "Policy API URL")
	pgHost := fs.String("pg-host", "postgres", "PostgreSQL host")
	pgPort := fs.Int("pg-port", 5432, "PostgreSQL port")
	pgDB := fs.String("pg-db", "foyer", "PostgreSQL database")
	pgUser := fs.String("pg-user", store.RadiusRole, "PostgreSQL user FreeRADIUS logs in as; its own least-privileged role, never the schema owner")
	// FOYER_POSTGRES_PASSWORD_FILE as well as the variable, through the same
	// helper cmd/foyer reads FOYER_KEK with. This value ends up in cleartext in
	// the generated mods-available/sql — rlm_sql has no other way to
	// authenticate — so keeping it out of `docker inspect` on the way there is
	// worth the one line.
	pgFromEnv, pgErr := config.EnvOrFile("FOYER_POSTGRES_PASSWORD")
	pgPassword := fs.String("pg-password", pgFromEnv, "PostgreSQL password")

	// The mTLS material on the rlm_rest boundary (docs/rules/security.md §2). These are
	// paths **inside the FreeRADIUS container**, not paths on the machine
	// running this command, so they are not checked for existence here — the
	// generator ties them to the scheme of --policy-url instead, and refuses to
	// write either half of the pair on its own.
	clientCert := fs.String("policy-client-cert", os.Getenv("FOYER_RADDB_POLICY_CLIENT_CERT"),
		"Certificate FreeRADIUS presents to the Policy API (required when --policy-url is https)")
	clientKey := fs.String("policy-client-key", os.Getenv("FOYER_RADDB_POLICY_CLIENT_KEY"),
		"Private key for --policy-client-cert")
	serverCA := fs.String("policy-server-ca", os.Getenv("FOYER_RADDB_POLICY_SERVER_CA"),
		"CA the Policy API's own certificate is checked against")

	// FreeRADIUS's thread pool is fixed once the server starts, so the generator
	// emits a `thread pool { }` section — without one this number is FreeRADIUS's
	// built-in default and nothing in deploy/ can reach it. It also sizes the rlm_sql and rlm_rest connection pools,
	// because a worker waiting on a pool is a worker not answering Status-Server.
	maxServers := fs.Int("max-servers", raddb.DefaultConfig().MaxServers,
		"FreeRADIUS worker threads; also the size of the SQL and Policy API connection pools")
	if err := fs.Parse(args); err != nil {
		return err
	}
	// After Parse, so that an explicit --pg-password still works when the
	// environment is misconfigured — but a flag left unset must not silently
	// inherit an empty value from a file nobody could read.
	if pgErr != nil && *pgPassword == "" {
		return pgErr
	}

	cfg := raddb.DefaultConfig()
	cfg.PolicyAPIURL = *policyURL
	cfg.PostgresHost = *pgHost
	cfg.PostgresPort = *pgPort
	cfg.PostgresDB = *pgDB
	cfg.PostgresUser = *pgUser
	cfg.PostgresPassword = *pgPassword
	cfg.PolicyClientCertFile = *clientCert
	cfg.PolicyClientKeyFile = *clientKey
	cfg.PolicyServerCAFile = *serverCA
	cfg.MaxServers = *maxServers

	if err := raddb.NewGenerator(cfg).Generate(*outDir); err != nil {
		return err
	}
	fmt.Printf("Generated FreeRADIUS 3.2.x configuration in %s\n", *outDir)
	return nil
}
