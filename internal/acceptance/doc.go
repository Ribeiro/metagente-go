// Package acceptance holds the black-box acceptance suite: it runs the
// command line against the scripts in testdata/script and checks what a
// person would see.
//
// The suite needs one dependency that the rest of the project does not, so it
// is built only with the `acceptance` tag:
//
//	go get github.com/rogpeppe/go-internal/testscript@latest
//	go test -tags acceptance ./internal/acceptance/...
package acceptance
