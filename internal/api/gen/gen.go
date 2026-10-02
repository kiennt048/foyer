// Package gen holds the server interface and models generated from
// `web/contract/openapi.yaml`.
//
// Nothing in this package is written by hand. `server.gen.go` is build output in
// exactly the sense `raddb/` is: if you find yourself editing it, the change
// belongs in the contract, and the contract is owned by the frontend phase.
//
// Why generate at all, when a hand-written router would be shorter: `web/mock`
// already carries the rule that a handler for a nonexistent operation does not
// compile, and generating StrictServerInterface is what extends that rule to Go.
// An operation added to the contract and not implemented here fails `go build`
// at the point `internal/api` asserts it satisfies the interface; an operation
// implemented here and absent from the contract has no method to satisfy. Both
// are compile errors rather than a 404 somebody finds in integration.
package gen

//go:generate go tool oapi-codegen -config cfg.yaml ../../../web/contract/openapi.yaml
